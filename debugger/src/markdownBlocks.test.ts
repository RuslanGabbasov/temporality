// Artifact content splitting (docs/presentation-engine.md ×
// docs/artifact-registry.md): presentation fences become presentation blocks,
// everything else stays markdown — ordered, lossless, and safe: an invalid
// fence renders as plain text instead of disappearing.
import { describe, expect, it } from 'vitest'
import { splitMarkdownBlocks } from './markdownBlocks'

describe('splitMarkdownBlocks', () => {
  const chartIR = '{"version":"1.0","type":"presentation","title":"Latency","content":[{"type":"kpi","items":[{"label":"p95","value":1}]}]}'

  it('splits prose and presentation fences in order', () => {
    const doc = `# Отчёт\n\nТекст до.\n\n\`\`\`presentation\n${chartIR}\n\`\`\`\n\nТекст после.\n`
    const blocks = splitMarkdownBlocks(doc)
    expect(blocks).toHaveLength(3)
    expect(blocks[0]).toEqual({ kind: 'markdown', text: '# Отчёт\n\nТекст до.\n' })
    expect(blocks[1].kind).toBe('presentation')
    if (blocks[1].kind === 'presentation') {
      expect((blocks[1].ir as { title?: string }).title).toBe('Latency')
    }
    expect(blocks[2]).toEqual({ kind: 'markdown', text: '\nТекст после.\n' })
  })

  it('keeps a broken fence visible as plain markdown', () => {
    const doc = 'intro\n```presentation\nnot json at all\n```\n'
    const blocks = splitMarkdownBlocks(doc)
    expect(blocks).toHaveLength(1)
    expect(blocks[0].kind).toBe('markdown')
    expect((blocks[0] as { text: string }).text).toContain('not json at all')
  })

  it('keeps an unterminated fence visible', () => {
    const doc = 'intro\n```presentation\nnever closed'
    const blocks = splitMarkdownBlocks(doc)
    expect(blocks).toHaveLength(1)
    expect(blocks[0].kind).toBe('markdown')
  })

  it('passes a document without fences through as one block', () => {
    const blocks = splitMarkdownBlocks('# Just text\n\nno fences')
    expect(blocks).toEqual([{ kind: 'markdown', text: '# Just text\n\nno fences' }])
  })
})
