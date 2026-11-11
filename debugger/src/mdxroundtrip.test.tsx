// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { describe, expect, it } from 'vitest'
import { MDXEditor } from '@mdxeditor/editor'
import { artifactEditorPlugins } from './ArtifactEditor'

const SAMPLE = [
  '# Заголовок',
  '',
  'Абзац с **жирным** и [workspace ссылкой](design/инструкция.md) и обычной https://example.com.',
  '',
  'Строка с жёстким переносом',
  'вторая строка.',
  '',
  '- пункт один',
  '- пункт два',
  '',
  '1. шаг',
  '',
  '> Цитата',
  '',
  '| A | B |',
  '|---|---|',
  '| 1 | 2 |',
  '',
  '```presentation slides',
  '{"slides": ["a"]}',
  '```',
  '',
  '```mermaid',
  'graph TD; A-->B;',
  '```',
  '',
  '```js',
  'const x = 1',
  '```',
  '',
  '---',
].join('\n')

function EditorUnderTest({ initial, onReady }: { initial: string; onReady: (get: () => string) => void }) {
  return (
    <MDXEditor
      markdown={initial}
      plugins={artifactEditorPlugins(false)}
      onChange={() => {}}
      ref={(editor) => { if (editor) onReady(() => editor.getMarkdown()) }}
    />
  )
}

describe('mdxeditor markdown round-trip', () => {
  it('preserves artifact-relevant constructs', () => {
    let get: (() => string) | null = null
    const container = document.createElement('div')
    document.body.appendChild(container)
    const root = createRoot(container)
    act(() => { root.render(<EditorUnderTest initial={SAMPLE} onReady={(g) => { get = g }} />) })
    expect(get).toBeTruthy()
    const out = get!()
    console.log('=== ROUNDTRIP OUTPUT ===\n' + out)
    expect(out).toContain('# Заголовок')
    expect(out).toContain('**жирным**')
    expect(out).toContain('```presentation')
    expect(out).toContain('```mermaid')
    expect(out).toContain('| A | B |')
    expect(out).toContain('graph TD; A-->B;')
  })
})
