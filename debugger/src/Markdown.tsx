import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import remarkBreaks from 'remark-breaks'
import MermaidDiagram from './MermaidDiagram'

const FILES_URL_PREFIX = '/kernel-api/v1/workspace/projects/'

// Segments that must never be linkified: fenced code blocks and inline code
// spans. Splitting on this pattern yields alternating plain/code segments.
const CODE_SEGMENT_RE = /(```[\s\S]*?```|~~~[\s\S]*?~~~|`[^`\n]+`)/g

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

// A workspace path referenced in prose: "/workspace/out/report.md" or
// "workspace/inbox/file.zip". The leading boundary avoids mid-word matches.
const WORKSPACE_PATH_RE = /(^|[\s(\[<"'])(\/?workspace\/[A-Za-z0-9][A-Za-z0-9._/-]*)/g

// Turns workspace paths outside code segments into real download links.
export function linkifyWorkspacePaths(content: string, projectId: string): string {
  return content
    .split(CODE_SEGMENT_RE)
    .map((segment, index) => {
      if (index % 2 === 1) return segment
      return segment.replace(WORKSPACE_PATH_RE, (_match, lead: string, rawPath: string) => {
        const path = rawPath.replace(/[._-]+$/, '')
        const trail = rawPath.slice(path.length)
        const url = `${FILES_URL_PREFIX}${encodeURIComponent(projectId)}/files?path=${encodeURIComponent(path)}`
        return `${lead}[${path}](${url})${trail}`
      })
    })
    .join('')
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

export default function Markdown({ content, onFile }: { content: string; onFile?: (path: string) => void }) {
  const segments = content.includes('```mermaid') ? splitMermaidSegments(content) : null
  if (segments) {
    return <div className="markdown-body">
      {segments.map((segment, index) => segment.kind === 'mermaid'
        ? <MermaidDiagram key={`m${index}`} chart={segment.chart} />
        : <MarkdownSegment key={`t${index}`} content={segment.text} onFile={onFile} />)}
    </div>
  }
  return <div className="markdown-body"><MarkdownSegment content={content} onFile={onFile} /></div>
}

function MarkdownSegment({ content, onFile }: { content: string; onFile?: (path: string) => void }) {
  return <ReactMarkdown
    remarkPlugins={[remarkGfm, remarkBreaks]}
    components={{
      a: ({ href, children }) => {
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
        return <a href={href} target="_blank" rel="noopener noreferrer">{children}</a>
      },
      img: ({ src, alt }) => <a className="markdown-image-link" href={src} target="_blank" rel="noopener noreferrer">Image: {alt || src}</a>,
    }}
  >{content}</ReactMarkdown>
}
