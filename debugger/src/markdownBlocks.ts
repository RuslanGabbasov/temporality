// Presentation fences inside artifact markdown (docs/presentation-engine.md
// × docs/artifact-registry.md): the artifact stores its readable source with
// presentation fences, and the viewer renders each fence through
// PresentationView — the same pattern Markdown.tsx applies to mermaid
// fences. This module splits a markdown document into ordered blocks without
// touching anything it cannot parse: a broken fence stays visible as plain
// markdown rather than disappearing.

export interface MarkdownBlock {
  kind: 'markdown'
  text: string
}

export interface PresentationBlock {
  kind: 'presentation'
  id: string
  ir: unknown
}

export type ContentBlock = MarkdownBlock | PresentationBlock

export function splitMarkdownBlocks(content: string): ContentBlock[] {
  const lines = content.split('\n')
  const blocks: ContentBlock[] = []
  let prose: string[] = []
  let counter = 0
  for (let i = 0; i < lines.length; i++) {
    if (lines[i].startsWith('```presentation')) {
      const end = lines.indexOf('```', i + 1)
      if (end < 0) {
        // Unterminated fence: not a block, keep the raw text visible.
        prose.push(...lines.slice(i))
        break
      }
      const payload = lines.slice(i + 1, end).join('\n').trim()
      try {
        const ir = JSON.parse(payload)
        if (ir && typeof ir === 'object') {
          if (prose.length > 0) {
            blocks.push({ kind: 'markdown', text: prose.join('\n') })
            prose = []
          }
          blocks.push({ kind: 'presentation', id: `fence-${counter++}`, ir })
          i = end
          continue
        }
      } catch {
        // Falls through: invalid JSON renders as plain markdown.
      }
    }
    prose.push(lines[i])
  }
  if (prose.length > 0) {
    blocks.push({ kind: 'markdown', text: prose.join('\n') })
  }
  return blocks
}
