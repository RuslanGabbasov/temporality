import { describe, expect, it } from 'vitest'
import { previewKindFor } from './filePreview'

// Which workspace documents the chat opens inline instead of downloading.
// A bare path link only helps when the extension is previewable — otherwise
// clicking it silently falls back to a download.
describe('previewKindFor', () => {
  it('previews the JS/TS module variants agents actually emit', () => {
    for (const path of ['scripts/fetch-mcp-secrets.mjs', 'src/util.cjs', 'src/a.mts', 'src/b.cts']) {
      expect(previewKindFor(path), path).toBe('text')
    }
  })

  it('previews the plain text and image documents', () => {
    // Markdown renders as markdown — the same surface the storage preview
    // uses — not as a highlighted code pane.
    expect(previewKindFor('docs/plan.md')).toBe('markdown')
    expect(previewKindFor('docs/plan.markdown')).toBe('markdown')
    expect(previewKindFor('out/report.txt')).toBe('text')
    expect(previewKindFor('out/chart.png')).toBe('image')
  })

  it('keeps everything else on the download path', () => {
    expect(previewKindFor('out/app.zip')).toBeNull()
    expect(previewKindFor('docs/notes')).toBeNull()
  })
})
