import { describe, expect, it } from 'vitest'
import { highlightCode, languageForFile } from './syntax'

describe('languageForFile', () => {
  it('maps common extensions to grammars', () => {
    expect(languageForFile('main.go')).toBe('go')
    expect(languageForFile('component.tsx')).toBe('typescript')
    expect(languageForFile('deploy.yml')).toBe('yaml')
    expect(languageForFile('Cargo.toml')).toBe('ini')
    expect(languageForFile('Dockerfile')).toBe('dockerfile')
    expect(languageForFile('schema.proto')).toBe('protobuf')
  })

  it('returns empty string for unknown extensions and dotfiles', () => {
    expect(languageForFile('report.pdf')).toBe('')
    expect(languageForFile('e2e-sandbox-runbook.md')).toBe('markdown')
    expect(languageForFile('Makefile')).toBe('')
  })
})

describe('highlightCode', () => {
  it('wraps known-language code in hljs spans and escapes the source', () => {
    const html = highlightCode('package main\n// привет\nfunc main() {}', 'go')
    expect(html).toContain('hljs-keyword')
    expect(html).toContain('hljs-comment')
    expect(html).toContain('привет') // non-ASCII survives
  })

  it('escapes HTML in unknown languages instead of highlighting', () => {
    const html = highlightCode('<script>alert("x")</script>', '')
    expect(html).not.toContain('<script>')
    expect(html).toContain('&lt;script&gt;')
  })

  it('never throws on empty input', () => {
    expect(highlightCode('', 'go')).toBe('')
  })
})
