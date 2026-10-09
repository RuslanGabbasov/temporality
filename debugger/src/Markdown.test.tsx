import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import Markdown, { linkifyWorkspacePaths } from './Markdown'

const URL_FOR = (projectId: string, path: string) =>
  `/kernel-api/v1/workspace/projects/${encodeURIComponent(projectId)}/files?path=${encodeURIComponent(path)}`

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
