import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import Markdown, { linkifyWorkspacePaths, splitMermaidSegments } from './Markdown'

const URL_FOR = (projectId: string, path: string) =>
  `/kernel-api/v1/workspace/projects/${encodeURIComponent(projectId)}/files?path=${encodeURIComponent(path)}`

describe('splitMermaidSegments', () => {
  it('splits ```mermaid blocks from the markdown between them', () => {
    const segments = splitMermaidSegments('# Intro\n\n```mermaid\nerDiagram\n  A ||--o{ B : has\n```\n\n## Next')
    expect(segments).toEqual([
      { kind: 'markdown', text: '# Intro\n\n' },
      { kind: 'mermaid', chart: 'erDiagram\n  A ||--o{ B : has\n' },
      { kind: 'markdown', text: '\n\n## Next' },
    ])
  })

  it('returns one markdown segment when no diagram is present', () => {
    expect(splitMermaidSegments('```js\nconst x = 1\n```')).toEqual([
      { kind: 'markdown', text: '```js\nconst x = 1\n```' },
    ])
  })
})

describe('Markdown (mermaid content)', () => {
  // renderToStaticMarkup of the async diagram component yields the
  // placeholder container; the chart itself renders client-side.
  it('renders a mermaid placeholder, not a code block', () => {
    const html = renderToStaticMarkup(<Markdown content={'```mermaid\nerDiagram\n  A ||--o{ B : has\n```'} />)
    expect(html).toContain('mermaid-diagram')
    expect(html).not.toContain('language-mermaid')
  })

  it('leaves plain markdown untouched', () => {
    const html = renderToStaticMarkup(<Markdown content={'# Just text'} />)
    expect(html).toContain('Just text')
    expect(html).not.toContain('mermaid-diagram')
  })
})

describe('Markdown', () => {
  it('renders common Markdown and GFM tables', () => {
    const html = renderToStaticMarkup(<Markdown content={'## Summary\n\n| Check | Result |\n| --- | --- |\n| build | **passed** |'} />)
    expect(html).toContain('<h2>Summary</h2>')
    expect(html).toContain('<table>')
    expect(html).toContain('<strong>passed</strong>')
  })

  // The ref-ui-ux-guidelines-web-p1 defect: a blockquote line glued to the
  // table header folded the whole table into one giant quote. The renderer
  // normalizes the missing blank line so the table renders as a table.
  it('separates a blockquote glued to a table so the table renders', () => {
    const content = '> Источник: ui-ux-pro-max-skill\n| Category | Do | Don\'t |\n|---|---|---|\n| Nav | smooth | none |'
    const html = renderToStaticMarkup(<Markdown content={content} />)
    expect(html).toContain('<table>')
    expect(html).toContain('<th>Category</th>')
    expect(html).toContain('<blockquote>')
  })

  it('does not touch tables already separated from blockquotes', () => {
    const content = '> note\n\n| A | B |\n|---|---|\n| 1 | 2 |'
    const html = renderToStaticMarkup(<Markdown content={content} />)
    expect(html).toContain('<table>')
    const quoteCount = (html.match(/<blockquote>/g) ?? []).length
    expect(quoteCount).toBe(1)
  })

  it('does not split a quote whose next line continues the quote text', () => {
    const content = '> line one\n> line two\n\n| A | B |\n|---|---|\n| 1 | 2 |'
    const html = renderToStaticMarkup(<Markdown content={content} />)
    expect(html).toContain('<table>')
  })

  it('does not render embedded raw HTML', () => {
    const html = renderToStaticMarkup(<Markdown content={'<script>alert("x")</script>\n\n[docs](https://example.com)'} />)
    expect(html).not.toContain('<script>')
    expect(html).toContain('target="_blank"')
    expect(html).toContain('rel="noopener noreferrer"')
  })
})

describe('linkifyWorkspacePaths', () => {
  it('links absolute and relative workspace paths', () => {
    expect(linkifyWorkspacePaths('see /workspace/out/report.md', 'p1'))
      .toBe(`see [${'/workspace/out/report.md'}](${URL_FOR('p1', '/workspace/out/report.md')})`)
    expect(linkifyWorkspacePaths('saved to workspace/inbox/file.zip', 'p1'))
      .toBe(`saved to [${'workspace/inbox/file.zip'}](${URL_FOR('p1', 'workspace/inbox/file.zip')})`)
  })

  it('leaves trailing punctuation outside the link', () => {
    expect(linkifyWorkspacePaths('done: /workspace/out/report.md.', 'p1'))
      .toBe(`done: [${'/workspace/out/report.md'}](${URL_FOR('p1', '/workspace/out/report.md')}).`)
    expect(linkifyWorkspacePaths('(/workspace/a.zip),', 'p1'))
      .toBe(`([${'/workspace/a.zip'}](${URL_FOR('p1', '/workspace/a.zip')})),`)
  })

  it('does not touch fenced code blocks or inline code', () => {
    const fenced = '```\n/workspace/out/report.md\n```'
    expect(linkifyWorkspacePaths(fenced, 'p1')).toBe(fenced)
    const inline = 'run `ls /workspace/out` first'
    expect(linkifyWorkspacePaths(inline, 'p1')).toBe(inline)
  })

  it('linkifies prose around code segments', () => {
    const input = 'report at /workspace/out/report.md:\n```\ncat /workspace/out/report.md\n```'
    const fence = '```'
    const expected = 'report at [' + '/workspace/out/report.md' + '](' + URL_FOR('p1', '/workspace/out/report.md') + '):\n' + fence + '\ncat /workspace/out/report.md\n' + fence
    expect(linkifyWorkspacePaths(input, 'p1')).toBe(expected)
  })

  it('ignores plain prose without workspace paths', () => {
    expect(linkifyWorkspacePaths('the file /etc/hosts and /tmp/x', 'p1'))
      .toBe('the file /etc/hosts and /tmp/x')
  })
})
