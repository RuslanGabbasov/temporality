import { describe, expect, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import RichDiff from './RichDiff'

describe('RichDiff', () => {
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
