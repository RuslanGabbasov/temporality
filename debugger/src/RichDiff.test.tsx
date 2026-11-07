import { describe, expect, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import RichDiff from './RichDiff'
import { diffText } from './textDiff'

describe('RichDiff', () => {
  it.each(['text', 'yaml'] as const)('highlights literal changes in %s', (format) => {
    const html = renderToStaticMarkup(<RichDiff format={format} oldText={'  key: Old\n  value: "<old>"'} newText={'    key: old\n  value: "<new>"'} />)
    expect(html).toContain('markdown_diff.what_changed')
    expect(html).toContain('text-decoration:line-through')
    expect(html).toContain('var(--tm-success) 22%')
    expect(html).toContain('&lt;')
    expect(html).toContain('white-space:pre-wrap;font-family:var(--tm-mono)')
  })

  it('preserves exact source in inline changes, including large regions', () => {
    for (const [before, after] of [
      ['  key: Value', '    key: value'],
      ['key: "*_value"\n\tother: 1', 'key: *_value\n  other: 2'],
      [Array(1100).fill('old').join('\n'), Array(1100).fill('new').join('\n')],
    ]) {
      const changes = diffText(before, after).rows[0].changes
      expect(changes).toBeDefined()
      expect(changes!.filter((c) => c.op !== 'add').map((c) => c.text).join('')).toBe(before)
      expect(changes!.filter((c) => c.op !== 'del').map((c) => c.text).join('')).toBe(after)
      expect(changes!.some((c) => c.op === 'del')).toBe(true)
      expect(changes!.some((c) => c.op === 'add')).toBe(true)
    }
  })

  it('compares YAML indentation and case literally and preserves source markup', () => {
    const html = renderToStaticMarkup(<RichDiff
      format="yaml"
      title="skill.yaml"
      oldLabel="v1"
      newLabel="v2"
      oldText={'runtime:\n  sandbox: Optional\n  value: "<old>"'}
      newText={'runtime:\n    sandbox: optional\n  value: "<tag>"'}
    />)
    expect(html).toContain('skill.yaml')
    expect(html).toContain('v1')
    expect(html).toContain('v2')
    expect(html).toContain('  sandbox: Optional')
    expect(html).toContain('    sandbox: optional')
    expect(html).toContain('&lt;tag&gt;')
    expect(html).toContain('<pre')
    expect(html).toContain('<code>')
    expect(html).not.toContain('diff-table')
    expect(html).not.toContain('markdown-body')
  })

  it('reports identical structured text without changes', () => {
    const html = renderToStaticMarkup(<RichDiff format="text" oldText="key: value" newText="key: value" />)
    expect(html).toContain('diff.no_changes')
    expect(html).not.toContain('<table')
  })

  it('renders Markdown by default', () => {
    const html = renderToStaticMarkup(<RichDiff oldText="" newText="# Instructions" />)
    expect(html).toContain('<h1>Instructions</h1>')
    expect(html).not.toContain('diff-table')
  })
})
