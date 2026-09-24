import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import Markdown from './Markdown'

describe('Markdown', () => {
  it('renders common Markdown and GFM tables', () => {
    const html = renderToStaticMarkup(<Markdown content={'## Summary\n\n| Check | Result |\n| --- | --- |\n| build | **passed** |'} />)
    expect(html).toContain('<h2>Summary</h2>')
    expect(html).toContain('<table>')
    expect(html).toContain('<strong>passed</strong>')
  })

  it('does not render embedded raw HTML', () => {
    const html = renderToStaticMarkup(<Markdown content={'<script>alert("x")</script>\n\n[docs](https://example.com)'} />)
    expect(html).not.toContain('<script>')
    expect(html).toContain('target="_blank"')
    expect(html).toContain('rel="noopener noreferrer"')
  })
})
