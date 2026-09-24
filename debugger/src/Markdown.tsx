import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'

export default function Markdown({ content }: { content: string }) {
  return <div className="markdown-body">
    <ReactMarkdown
      remarkPlugins={[remarkGfm]}
      components={{
        a: ({ href, children }) => <a href={href} target="_blank" rel="noopener noreferrer">{children}</a>,
        img: ({ src, alt }) => <a className="markdown-image-link" href={src} target="_blank" rel="noopener noreferrer">Image: {alt || src}</a>,
      }}
    >{content}</ReactMarkdown>
  </div>
}
