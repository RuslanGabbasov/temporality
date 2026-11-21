import { readFileSync } from 'fs'
import { describe, expect, it } from 'vitest'

// A CSS variable that is used but never defined silently degrades to the
// property's fallback: a fill with the undefined `--tm-text-1` painted
// chart labels BLACK on the dark canvas (the «чёрный текст на тёмном
// фоне» complaint) — an undefined var in a presentation attribute has no
// fallback at all. This guard scans every source string for var(--tm-…) and
// requires the name to be declared in carbon.scss (the only stylesheet the
// app loads). The scss itself cannot be imported (?raw on .scss comes back
// empty through vitest's CSS pipeline), so it is read from disk — both the
// debugger cwd and the repo-root cwd are accepted.

const sources = import.meta.glob('./*.{ts,tsx}', { query: '?raw', import: 'default', eager: true }) as Record<string, string>

function readCss(): string {
  for (const candidate of ['src/carbon.scss', 'debugger/src/carbon.scss']) {
    try {
      return readFileSync(candidate, 'utf8')
    } catch { /* try the next cwd */ }
  }
  return ''
}

const css = readCss()
const defined = new Set([...css.matchAll(/(--tm-[a-z0-9-]+)\s*:/g)].map((m) => m[1]))

describe('theme variable hygiene', () => {
  it('every var(--tm-…) used in src or in carbon.scss itself is defined', () => {
    expect(css.length).toBeGreaterThan(1000) // the stylesheet really loaded
    const missing: string[] = []
    const scan = (path: string, content: string) => {
      for (const match of content.matchAll(/var\((--tm-[a-z0-9-]+)/g)) {
        if (!defined.has(match[1])) missing.push(`${path}: ${match[1]}`)
      }
    }
    for (const [path, content] of Object.entries(sources)) scan(path, content)
    scan('carbon.scss', css)
    expect(missing).toEqual([])
  })

  it('the stylesheet really declares the primary text variables', () => {
    expect(defined.size).toBeGreaterThan(50)
    expect(defined.has('--tm-text')).toBe(true)
    expect(defined.has('--tm-text-2')).toBe(true)
  })

  it('wide presentation charts are not width-capped (squares are)', () => {
    // The 480px cap kept bar/line/heatmap charts from filling the column;
    // only square formats keep a cap (pie/gauge/radar).
    const rule = css.match(/\.presentation-chart-svg\s*\{[^}]*\}/)?.[0] ?? ''
    expect(rule).toContain('width: 100%')
    expect(rule).not.toContain('max-width: 480px')
    expect(css).toContain('.presentation-chart-radar')
  })
})
