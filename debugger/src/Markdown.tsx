import { memo } from 'react'
import ReactMarkdown, { defaultUrlTransform } from 'react-markdown'
import remarkGfm from 'remark-gfm'
import remarkBreaks from 'remark-breaks'
import MermaidDiagram from './MermaidDiagram'
import { IN_APP_STORAGE_PATH, resolveImageSource, useResolvedImage } from './imageResolution'

const FILES_URL_PREFIX = '/kernel-api/v1/workspace/projects/'

// Segments that must never be linkified: fenced code blocks, inline code
// spans — and markdown links/images. A `![alt](/workspace/x.png)` or
// `[text](docs/x.md)` is ALREADY a link: wrapping the path inside its
// parentheses nests one markdown link inside another, the renderer decodes
// it as literal text (the «картинки перестали рендериться» regression of the
// artifacts linkify pass) — so link syntax splits out untouched, exactly
// like code does.
const CODE_SEGMENT_RE = /(```[\s\S]*?```|~~~[\s\S]*?~~~|`[^`\n]+`)/g
const MARKDOWN_LINK_SEGMENT_RE = /(!\[[^\]]*\]\([^)]*\)|\[[^\]]*\]\([^)]*\))/g

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

// A workspace path referenced in prose. Two shapes: the sandbox-absolute form
// agents write ("/workspace/out/report.md", "workspace/inbox/file.zip") and
// a bare relative path ("scripts/fetch-mcp-secrets.mjs") — the second needs
// at least one directory step and a real extension, so prose like "v1.2.3",
// "/etc/hosts", "and/or" or "github.com/org/repo" stays untouched. The
// leading boundary avoids mid-word matches; a URL never matches because ":"
// is neither a boundary nor a path character.
const WORKSPACE_PATH_RE = /(^|[\s(\[<"«'])(\/?workspace\/[A-Za-z0-9][A-Za-z0-9._/-]*|[A-Za-z0-9_-]+(?:\/[A-Za-z0-9_.-]+)+\.[A-Za-z0-9]{2,12})/g

// The path the file API resolves inside the project's workspace area: it is
// always area-relative ("out/report.md"). The API strips a leading
// "/workspace" but not the bare "workspace/" form, so both are normalized
// here — otherwise "workspace/inbox/f.zip" 404s — and the href matches what
// the storage file route expects as well.
function fileApiPath(path: string): string {
  return path.replace(/^\/?workspace\//, '')
}

// Turns workspace paths outside code segments and markdown links into real
// download links.
export function linkifyWorkspacePaths(content: string, projectId: string): string {
	return content
		.split(CODE_SEGMENT_RE)
		.map((segment, index) => {
			if (index % 2 === 1) return segment
			return segment
				.split(MARKDOWN_LINK_SEGMENT_RE)
				.map((part, partIndex) => (partIndex % 2 === 1 ? part : linkifyPlainPaths(part, projectId)))
				.join('')
		})
		.join('')
}

// The inner transform: wrap bare workspace paths of one plain-text piece.
function linkifyPlainPaths(segment: string, projectId: string): string {
	return segment.replace(WORKSPACE_PATH_RE, (_match, lead: string, rawPath: string) => {
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
