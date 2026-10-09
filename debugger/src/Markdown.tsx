import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import remarkBreaks from 'remark-breaks'

const FILES_URL_PREFIX = '/kernel-api/v1/workspace/projects/'

// Segments that must never be linkified: fenced code blocks and inline code
// spans. Splitting on this pattern yields alternating plain/code segments.
const CODE_SEGMENT_RE = /(```[\s\S]*?```|~~~[\s\S]*?~~~|`[^`\n]+`)/g

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
  return <div className="markdown-body">
    <ReactMarkdown
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
  </div>
}
