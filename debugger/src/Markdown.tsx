import { memo } from 'react'
import ReactMarkdown, { defaultUrlTransform } from 'react-markdown'
import remarkGfm from 'remark-gfm'
import remarkBreaks from 'remark-breaks'
import MermaidDiagram from './MermaidDiagram'
import { IN_APP_STORAGE_PATH, resolveImageSource, useResolvedImage } from './imageResolution'

const FILES_URL_PREFIX = '/kernel-api/v1/workspace/projects/'

// Segments that must never be linkified: fenced code blocks and markdown
// links/images. A `![alt](/workspace/x.png)` or `[text](docs/x.md)` is ALREADY
// a link: wrapping the path inside its parentheses nests one markdown link
// inside another, the renderer decodes it as literal text (the «картинки
// перестали рендериться» regression of the artifacts linkify pass) — so link
// syntax splits out untouched. Fenced blocks stay code — their paths are
// examples, not references.
const FENCED_SEGMENT_RE = /(```[\s\S]*?```|~~~[\s\S]*?~~~)/g
const MARKDOWN_LINK_SEGMENT_RE = /(!\[[^\]]*\]\([^)]*\)|\[[^\]]*\]\([^)]*\))/g
// Inline code spans are the OPPOSITE: «файл в `docs/spec.md`» and "как код
// оформил" are exactly how people and agents point at files — a span whose
// content contains a workspace path is unwrapped and its paths linkified
// (a markdown link INSIDE the backticks would render as literal text, so the
// span loses its ticks around the link). Spans without a path stay verbatim.
const INLINE_CODE_SEGMENT_RE = /(`[^`\n]+`)/g

// ```mermaid fenced blocks render as diagrams, not as code — agents emit
// them in artifacts and answers (ER charts, sequences, state machines).
const MERMAID_SEGMENT_RE = /```mermaid\n([\s\S]*?)```/g

/** Split content into mermaid chart bodies and the markdown between them.
 *  Pure, exported for tests. */
export function splitMermaidSegments(content: string): Array<{ kind: 'markdown'; text: string } | { kind: 'mermaid'; chart: string }> {
  const segments: Array<{ kind: 'markdown'; text: string } | { kind: 'mermaid'; chart: string }> = []
  let cursor = 0
  for (const match of content.matchAll(MERMAID_SEGMENT_RE)) {
    const start = match.index ?? 0
    if (start > cursor) segments.push({ kind: 'markdown', text: content.slice(cursor, start) })
    segments.push({ kind: 'mermaid', chart: match[1] })
    cursor = start + match[0].length
  }
  if (cursor < content.length) segments.push({ kind: 'markdown', text: content.slice(cursor) })
  return segments
}

// A workspace path referenced in prose. Three shapes: the sandbox-absolute
// form agents write ("/workspace/out/report.md", "workspace/inbox/file.zip"),
// a root-absolute form inside the workspace — «/docs/client-vulns-inventory.md»
// (leading slash, at least one directory step, a real extension: /etc/hosts or
// /api/v1/events stay untouched) — and a bare relative path
// ("scripts/fetch-mcp-secrets.mjs") with the same directory+extension floor,
// so prose like "v1.2.3" or "github.com/org/repo" stays untouched. The leading
// boundary avoids mid-word matches; markdown emphasis characters (* _ ~ = +)
// are boundaries too — bolded file names ("**docs/spec.md**", the most common
// shape in agent answers and artifact bodies) must linkify; a URL never
// matches because ":" is neither a boundary nor a path character.
const WORKSPACE_PATH_RE = /(^|[\s(\[<"«'*_~=+])(\/?workspace\/[A-Za-z0-9][A-Za-z0-9._/-]*|\/(?:[A-Za-z0-9_-]+\/)+[A-Za-z0-9_.-]+\.[A-Za-z0-9]{2,12}|[A-Za-z0-9_-]+(?:\/[A-Za-z0-9_.-]+)+\.[A-Za-z0-9]{2,12})/g

// The path the file API resolves inside the project's workspace area: it is
// always area-relative ("out/report.md"). Stripped: a leading "/workspace",
// the bare "workspace/" form and the root-absolute leading slash — otherwise
// "/docs/x.md" and "workspace/inbox/f.zip" 404 — and the href matches what
// the storage file route expects as well.
function fileApiPath(path: string): string {
	return path.replace(/^\/?workspace\//, '').replace(/^\//, '')
}

// Turns workspace paths outside fenced blocks and markdown links into real
// download/preview links — including inside inline code spans, the form
// people and agents use most («как код оформил»).
export function linkifyWorkspacePaths(content: string, projectId: string): string {
	return content
		.split(FENCED_SEGMENT_RE)
		.map((segment, index) => {
			if (index % 2 === 1) return segment
			return segment
				.split(MARKDOWN_LINK_SEGMENT_RE)
				.map((part, partIndex) => (partIndex % 2 === 1 ? part : linkifyPlainPaths(part, projectId)))
				.join('')
		})
		.join('')
}

// The outer transform of one plain-text piece: wrap bare workspace paths, and
// unwrap inline code spans that contain a FILE path (the span's backticks
// would render a markdown link as literal text). A code span with a pathless
// object (`ls /workspace/out` — a directory, a command) stays verbatim.
function linkifyPlainPaths(segment: string, projectId: string): string {
	return segment
		.split(INLINE_CODE_SEGMENT_RE)
		.map((chunk, index) => {
			if (index % 2 === 1) {
				const inner = chunk.slice(1, -1)
				if (containsFilePath(inner)) {
					WORKSPACE_PATH_RE.lastIndex = 0
					return wrapPaths(inner, projectId)
				}
				return chunk
			}
			return wrapPaths(chunk, projectId)
		})
		.join('')
}

// containsFilePath reports whether the text holds a workspace path that ends
// in a file (the last segment carries an extension): those are references a
// reader can open. Directory-ish and command-like spans stay code.
function containsFilePath(text: string): boolean {
	const re = new RegExp(WORKSPACE_PATH_RE.source, 'g')
	let match: RegExpExecArray | null
	while ((match = re.exec(text)) !== null) {
		const last = (match[2].split('/').pop() ?? '')
		if (/\.[A-Za-z0-9]{2,12}$/.test(last)) return true
	}
	return false
}

// wrapPaths wraps every bare workspace path of one plain-text piece into a
// markdown link. Trailing punctuation (a sentence dot, an underscore run)
// stays outside the link.
function wrapPaths(text: string, projectId: string): string {
	return text.replace(WORKSPACE_PATH_RE, (_match, lead: string, rawPath: string) => {
		const path = rawPath.replace(/[._-]+$/, '')
		const trail = rawPath.slice(path.length)
		const url = `${FILES_URL_PREFIX}${encodeURIComponent(projectId)}/files?path=${encodeURIComponent(fileApiPath(path))}`
		return `${lead}[${path}](${url})${trail}`
	})
}

function pathFromFilesUrl(href: string): string | null {
  try {
    if (!href.startsWith(FILES_URL_PREFIX)) return null
    const url = new URL(href, window.location.origin)
    return url.searchParams.get('path')
  } catch {
    return null
  }
}

// In-document links: «See [Setup](#setup)» — handled by the reader surface
// (the artifacts page scrolls to the section), never a new tab.
function isInternalHref(href: string | undefined): boolean {
  return !!href && href.startsWith('#')
}

// Cross-artifact links: [Runbook](artifact:a1). The artifacts page opens the
// referenced document in place; react-markdown's default URL transform
// strips unknown schemes, so the artifact: scheme is allow-listed here.
export function artifactUrlTransform(url: string): string {
  if (/^artifact:(?:\/\/)?[A-Za-z0-9](?:[A-Za-z0-9._-]*[A-Za-z0-9_-])?$/.test(url)) return url
  return defaultUrlTransform(url)
}

// A blockquote line immediately followed by a table line (no blank line
// between) makes CommonMark fold the WHOLE table into the quote as lazy
// continuation — agents emit this shape now and then and the table renders as
// one giant quoted paragraph (artifact ref-ui-ux-guidelines-web-p1). The fix
// is one blank line after the quote; pure, exported for tests.
export function normalizeBlockquoteTable(content: string): string {
  const lines = content.split('\n')
  const out: string[] = []
  let inCode = false
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i]
    if (/^\s*(```|~~~)/.test(line)) inCode = !inCode
    out.push(line)
    if (inCode) continue
    const isQuote = /^\s*>\s?\S/.test(line)
    const next = lines[i + 1]
    const nextIsTable = next !== undefined && /^\s*\|/.test(next)
    const nextNotBlank = next !== undefined && next.trim() !== ''
    if (isQuote && nextNotBlank && nextIsTable) {
      out.push('')
    }
  }
  return out.join('\n')
}

// Memoized: the chat re-renders on every token delta, stream line and the
// 1Hz metrics ticker — without memo, EVERY bubble's markdown re-parses on
// each of those (a long streaming answer re-parsing dozens of times per
// second is the main source of the streaming bubble's jitter). content is
// the full parse input, so comparing it is exact; onFile and project
// participate too — Workspace passes a useCallback-stabilized handler and a
// stable project id.
export default memo(MarkdownBase, (prev, next) => prev.content === next.content && prev.onFile === next.onFile && prev.project === next.project)

function MarkdownBase({ content, onFile, project }: { content: string; onFile?: (path: string) => void; project?: string }) {
  const normalized = normalizeBlockquoteTable(content)
  const segments = normalized.includes('```mermaid') ? splitMermaidSegments(normalized) : null
  if (segments) {
    return <div className="markdown-body">
      {segments.map((segment, index) => segment.kind === 'mermaid'
        ? <MermaidDiagram key={`m${index}`} chart={segment.chart} />
        : <MarkdownSegment key={`t${index}`} content={segment.text} onFile={onFile} project={project} />)}
    </div>
  }
  return <div className="markdown-body"><MarkdownSegment content={normalized} onFile={onFile} project={project} /></div>
}

// A markdown image. In-app references (`/workspace/<rel>`, `/cache/<rel>`) sit
// behind the kernel storage API, which wants the auth header — a bare <img>
// 404s on this origin — so they are fetched through useResolvedImage and shown
// from a blob URL (the same pattern the presentation engine uses, PresentationView
// ImageView). Until the fetch settles — and after a failed one — the old link
// fallback stays, pointing at the real storage endpoint instead of the raw
// sandbox path. http(s) images render as plain <img>.
function MarkdownImage({ src = '', alt, project }: { src?: string; alt?: string; project?: string }) {
  // A bare relative src ("design-mockups/x.png") is a sandbox workspace path,
  // same convention linkifyWorkspacePaths uses — normalize it so it resolves
  // through the storage API instead of 404-ing on the debugger origin.
  const normalized = !src ? '' : /^(?:[a-z][a-z0-9+.-]*:|\/\/|\/)/i.test(src) ? src : `/workspace/${src}`
  const ref = resolveImageSource(normalized, project)
  const resolved = useResolvedImage(normalized, project)
  if (!src) return null
  const label = alt || src
  if (ref.storage) {
    if (!resolved || resolved === ref.src) {
      return <a className="markdown-image-link" href={ref.storage.url} target="_blank" rel="noopener noreferrer">Image: {label}</a>
    }
    return <img src={resolved} alt={label} loading="lazy" />
  }
  if (IN_APP_STORAGE_PATH.test(normalized)) {
    // In-app path but no project to resolve it against — a plain <img> would
    // 404 here, keep the link.
    return <a className="markdown-image-link" href={normalized} target="_blank" rel="noopener noreferrer">Image: {label}</a>
  }
  return <img src={normalized} alt={label} loading="lazy" />
}

const MarkdownSegment = memo(function MarkdownSegment({ content, onFile, project }: { content: string; onFile?: (path: string) => void; project?: string }) {
  return <ReactMarkdown
    remarkPlugins={[remarkGfm, remarkBreaks]}
    urlTransform={artifactUrlTransform}
    components={{
      a: ({ node: _node, href, children, ...rest }) => {
        const filePath = href ? pathFromFilesUrl(href) : null
        if (filePath && onFile) {
          return (
            <a
              href={href}
              className="workspace-file-link"
              title={filePath}
              onClick={(e) => { e.preventDefault(); onFile(filePath) }}
            >{children} ⬇</a>
          )
        }
        // Anchors (#section, footnote refs/backrefs) and artifact: links open
        // in place — a new tab there would orphan the reader mid-document.
        // The rest keep the rest-props: footnote refs carry ids and
        // data-footnote-ref that the native footnote UX relies on.
        if (isInternalHref(href) || (href && href.startsWith('artifact:'))) {
          return <a href={href} {...rest}>{children}</a>
        }
        return <a href={href} target="_blank" rel="noopener noreferrer" {...rest}>{children}</a>
      },
      img: ({ src, alt }) => <MarkdownImage src={src} alt={alt} project={project} />,
    }}
  >{content}</ReactMarkdown>
})
