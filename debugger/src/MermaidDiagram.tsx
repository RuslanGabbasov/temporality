import { useEffect, useId, useRef, useState } from 'react'

/** Repair the attribute-list shortcuts models love in erDiagram bodies:
 *  several attribute names sharing one type on a line (`string a b c`) and
 *  `;`-joined attributes (`ER { string a b; int c }`). Mermaid wants one
 *  `type name` per line. Quote-aware; lines carrying key markers (PK/FK/UK)
 *  or a quoted comment are left alone, so a valid diagram passes through
 *  unchanged. The result is parse-checked before rendering anyway — on any
 *  doubt the original degrades to <pre>. */
export function normalizeErDiagram(chart: string): string {
  if (!chart.trimStart().startsWith('erDiagram')) return chart
  const out: string[] = []
  let depth = 0
  for (const line of chart.split('\n')) {
    if (depth === 0) {
      const open = line.indexOf('{')
      if (open === -1) { out.push(line); continue }
      // Crow's-foot relationship lines (`A ||--o{ B : label`) carry a brace
      // in the notation itself — everything before it is relationship
      // syntax (`--`/`..`), never an entity name.
      const head = line.slice(0, open)
      if (head.includes('--') || head.includes('..')) { out.push(line); continue }
      out.push(line.slice(0, open + 1))
      depth = 1
      const rest = line.slice(open + 1)
      const close = rest.lastIndexOf('}')
      if (close !== -1) {
        pushExpanded(out, rest.slice(0, close))
        out.push('}')
        const tail = rest.slice(close + 1)
        if (tail.trim()) out.push(tail)
        depth = 0
      } else if (rest.trim()) {
        pushExpanded(out, rest)
      }
      continue
    }
    const close = line.indexOf('}')
    if (close === -1) { pushExpanded(out, line); continue }
    const indent = line.slice(0, line.length - line.trimStart().length)
    const before = line.slice(0, close)
    if (before.trim()) pushExpanded(out, before)
    out.push(`${indent}}`)
    depth = 0
    const tail = line.slice(close + 1)
    if (tail.trim()) out.push(tail)
  }
  return out.join('\n')
}

/** Split one entity-body chunk into attribute lines: `;`-separated pieces,
 *  each expanded when it packs several names onto one shared type. Original
 *  indentation carries over, so valid charts round-trip byte-identical. */
function pushExpanded(out: string[], chunk: string): void {
  for (const piece of splitQuoted(chunk, ';')) {
    if (!piece.trim()) continue
    const indent = piece.slice(0, piece.length - piece.trimStart().length)
    const tokens = tokenize(piece)
    if (tokens.length <= 2) { out.push(indent + piece.trim()); continue }
    // `type name PK "comment"` is a valid line — key markers and quoted
    // comments make expansion wrong, so only bare name lists expand.
    const decorated = tokens.slice(2).some((tok) => tok.startsWith('"') || ['PK', 'FK', 'UK'].includes(tok))
    if (decorated) { out.push(indent + piece.trim()); continue }
    for (const name of tokens.slice(1)) out.push(`${indent}${tokens[0]} ${name}`)
  }
}

/** Whitespace tokenizer that keeps double-quoted spans (comments) whole. */
function tokenize(text: string): string[] {
  const tokens: string[] = []
  let current = ''
  let inQuote = false
  for (const ch of text.trim()) {
    if (ch === '"') { inQuote = !inQuote; current += ch; continue }
    if (ch === ' ' && !inQuote) {
      if (current) { tokens.push(current); current = '' }
      continue
    }
    current += ch
  }
  if (current) tokens.push(current)
  return tokens
}

/** Split on a separator that is ignored inside double-quoted spans. */
function splitQuoted(text: string, sep: string): string[] {
  const parts: string[] = []
  let current = ''
  let inQuote = false
  for (const ch of text) {
    if (ch === '"') inQuote = !inQuote
    if (ch === sep && !inQuote) { parts.push(current); current = ''; continue }
    current += ch
  }
  parts.push(current)
  return parts
}

/** Node ids the flowchart lexer RESERVES (probed against mermaid 11.17):
 *  a bare `graph`/`end`/`style`… in id position lexes as its keyword token
 *  («got 'GRAPH'») and the whole chart fails — models name nodes `graph`
 *  all the time. Renamed to `<id>_` on the second parse attempt only. */
const FLOWCHART_RESERVED = new Set(['graph', 'flowchart', 'subgraph', 'end', 'style', 'linkStyle', 'classDef', 'class', 'click'])

/** Line-leading keywords followed by whitespace open a STATEMENT
 *  (`subgraph s`, `style A fill:…`, `class A foo`) — the keyword itself must
 *  survive; the same word in id position gets renamed. */
const FLOWCHART_STATEMENTS = new Set(['subgraph', 'style', 'linkStyle', 'classDef', 'class', 'click', 'end'])

/** Rename reserved words used as flowchart node ids so a model naming a
 *  node `graph` still renders. Only words OUTSIDE quotes, brackets (label
 *  text parses fine there), `|edge labels|` and `%%` comments are touched;
 *  the diagram-type line (`flowchart TB` / legacy `graph TB`) and bare `end`
 *  subgraph terminators are preserved. Callers run this only AFTER a parse
 *  failure — a valid chart is never rewritten. Exported for tests. */
export function repairFlowchartIds(chart: string): string {
  const lines = chart.split('\n')
  if (!/^(flowchart|graph)\b/.test((lines[0] ?? '').trimStart())) return chart
  return lines.map((line, index) => (index === 0 ? line : repairFlowchartLine(line))).join('\n')
}

function repairFlowchartLine(line: string): string {
  // The subgraph terminator — never an id, whatever follows on the line.
  if (line.trim() === 'end') return line
  const isWord = (ch: string | undefined) => !!ch && /[A-Za-z0-9_]/.test(ch)
  const shapeOpeners = new Set(['[', '{', '('])
  let out = ''
  let depth = 0
  let i = 0
  while (i < line.length) {
    const ch = line[i]
    if (ch === '"') {
      const close = line.indexOf('"', i + 1)
      const end = close === -1 ? line.length : close + 1
      out += line.slice(i, end)
      i = end
      continue
    }
    if (ch === '%' && line[i + 1] === '%') { out += line.slice(i); break }
    if (ch === '|') {
      const close = line.indexOf('|', i + 1)
      const end = close === -1 ? line.length : close + 1
      out += line.slice(i, end)
      i = end
      continue
    }
    if (ch === '[' || ch === '{' || ch === '(') { depth++; out += ch; i++; continue }
    if (ch === ']' || ch === '}' || ch === ')') { depth = Math.max(0, depth - 1); out += ch; i++; continue }
    if (isWord(ch)) {
      const start = i
      while (i < line.length && isWord(line[i])) i++
      const word = line.slice(start, i)
      const lineLead = out.trim() === ''
      const next = line[i]
      // A statement keyword at line start survives unless it carries a node
      // shape right away (`style["x"]` is an id, `style A …` is a statement).
      const statement = lineLead && FLOWCHART_STATEMENTS.has(word) && !(next && shapeOpeners.has(next))
      out += depth === 0 && FLOWCHART_RESERVED.has(word) && !statement ? `${word}_` : word
      continue
    }
    out += ch
    i++
  }
  return out
}

/** Convert mermaid's inline width cap into a fixed width.
 *
 *  The useMaxWidth path emits `<svg width="100%" style="max-width: Npx">`:
 *  the cap (an inline style, which no stylesheet can outrank without
 *  !important) pins the drawing at its natural size, so a diagram narrower
 *  than the text column never fills it. Rewriting `max-width: Npx` to
 *  `width: Npx` keeps N as the drawing's own size while the CSS
 *  `min-width: 100%` stretches narrow diagrams to the column (viewBox
 *  scales) and leaves wide ones at N to scroll — see .mermaid-diagram svg.
 *  Exported for tests. */
export function tuneSvgWidth(svg: string): string {
  return svg.replace(
    /^(\s*<svg\b[^>]*?\bstyle=")([^"]*?)max-width:\s*([\d.]+)px([^"]*")/,
    (_match, head: string, before: string, width: string, tail: string) => {
      // Keep every declaration that surrounded the cap; make sure the new
      // `width:` declaration is terminated when something follows it.
      const prefix = before && !before.endsWith(';') ? `${before};` : before
      return `${head}${prefix}width: ${width}px${tail}`
    },
  )
}

/** Mermaid theme bridge: `theme: 'base'` accepts full themeVariables, so the
 *  diagram is painted from the app's `--tm-*` tokens (read at render time via
 *  getComputedStyle) instead of mermaid's fixed `neutral` palette — nodes,
 *  clusters, edges and labels stay readable in both themes and match the
 *  hand-rolled presentation charts. `darkMode` follows the `data-theme`
 *  attribute theme.tsx writes on <html>. */
export function mermaidTheme(): { darkMode: boolean; themeVariables: Record<string, string> } {
  const styles = getComputedStyle(document.documentElement)
  const token = (name: string, fallback: string) => styles.getPropertyValue(name).trim() || fallback
  const darkMode = document.documentElement.getAttribute('data-theme') !== 'light'
  const background = token('--tm-bg', darkMode ? '#0F1F1D' : '#F7F5F0')
  const elevated = token('--tm-elevated', background)
  const raised = token('--tm-raised', elevated)
  const border = token('--tm-border', darkMode ? 'rgba(237,232,223,0.10)' : 'rgba(15,31,29,0.12)')
  const borderStrong = token('--tm-border-strong', border)
  const text = token('--tm-text', darkMode ? '#EDE8DF' : '#0F1F1D')
  const text2 = token('--tm-text-2', text)
  const text3 = token('--tm-text-3', text2)
  const teal = token('--tm-teal', '#2A6B63')
  const chart = (n: number) => token(`--tm-chart-${n}`, teal)
  // Mindmap (and git-graph) paint branches, node fills and labels from the
  // cScale/git ramp — mermaid's defaults there are the dark git palette, the
  // «страшно чёрные» mindmaps. The brand ramp gives every branch its chart
  // color (xmind-like), and the label color is picked for contrast on the
  // fill — the yellow band gets dark text, the blue band white.
  const ramp: Record<string, string> = {}
  for (let n = 1; n <= 8; n++) {
    const fill = chart(((n - 1) % 6) + 1)
    ramp[`cScale${n}`] = fill
    ramp[`cScaleInv${n}`] = fill
    // The neo look paints the node BOXES with mainBkg (the vivid colors live
    // in the gradient borders and edges), so node labels must read against
    // the canvas color — not against the band. The root's tspan variant is
    // the one label that sits ON a vivid fill (the teal box).
    ramp[`cScaleLabel${n}`] = text
  }
  ramp.cScale0 = teal
  ramp.cScaleLabel0 = readableOnFill(teal)
  return {
    darkMode,
    themeVariables: {
      background,
      fontFamily: token('--tm-font', styles.fontFamily || 'sans-serif'),
      // Node fill / text / border from the surface hierarchy.
      primaryColor: elevated,
      primaryTextColor: text,
      primaryBorderColor: borderStrong,
      secondaryColor: raised,
      secondaryTextColor: text,
      tertiaryColor: background,
      tertiaryTextColor: text2,
      mainBkg: elevated,
      nodeBorder: borderStrong,
      clusterBkg: background,
      clusterBorder: border,
      titleColor: text,
      textColor: text,
      // Edges need more contrast than a hairline border: the teal reads on
      // light backgrounds, the muted text tone on dark ones.
      lineColor: darkMode ? text3 : teal,
      edgeLabelBackground: background,
      noteBkgColor: raised,
      noteTextColor: text,
      noteBorderColor: border,
      // Categorical fills for pie/donut diagrams match ChartView's palette.
      pie1: chart(1), pie2: chart(2), pie3: chart(3), pie4: chart(4), pie5: chart(5), pie6: chart(6),
      pie7: chart(1), pie8: chart(2),
      pieTitleTextColor: text,
      pieLegendTextColor: text2,
      pieSectionTextColor: background,
      pieOuterStrokeColor: border,
      pieStrokeColor: background,
      // ER attribute rows: mermaid derives their fill from primaryColor and
      // lands on light pastels that clash with the theme text («white on
      // light» inside entity boxes). Explicit subtle tints keep the
      // attribute text readable in both themes.
      attributeBackgroundColorEven: darkMode ? 'rgba(255,255,255,0.05)' : 'rgba(15,31,29,0.05)',
      attributeBackgroundColorOdd: darkMode ? 'rgba(255,255,255,0.10)' : 'rgba(15,31,29,0.09)',
      // Branch palette for mindmap/git-graph (see the ramp above).
      git0: teal,
      gitBranchLabel0: readableOnFill(teal),
      THEME_COLOR_LIMIT: '8',
      ...ramp,
    },
  }
}

/** Picks a readable label color for text ON a brand fill — the same
 *  luminance rule as the on-color helpers of the design system: yellow
 *  bands get dark text, blue/purple bands get light text. Exported for
 *  tests. */
export function readableOnFill(fill: string): string {
  const m = fill.trim().match(/^#([0-9a-fA-F]{6})$/)
  if (!m) return '#FBF8F2'
  const n = parseInt(m[1], 16)
  const linear = (v: number) => {
    const s = v / 255
    return s <= 0.03928 ? s / 12.92 : Math.pow((s + 0.055) / 1.055, 2.4)
  }
  const luminance = 0.2126 * linear((n >> 16) & 255) + 0.7152 * linear((n >> 8) & 255) + 0.0722 * linear(n & 255)
  return luminance > 0.45 ? '#14201D' : '#FBF8F2'
}

/** Renders one ```mermaid fenced block as an SVG diagram.
 *
 *  the mermaid bundle (~1 MB) is loaded lazily on the first diagram — pages
 *  without diagrams never pay for it. Rendering failures (a malformed chart
 *  from a model) degrade to the chart source in a <pre>, never break the
 *  artifact. Charts are parse-checked BEFORE render: a failing render paints
 *  mermaid's giant error bomb into the document even though it also throws.
 *  A chart failing on a reserved word used as a node id (`graph`, `end`,
 *  `style`…) gets one repair attempt (repairFlowchartIds) before degrading.
 *  mermaid's own securityLevel stays 'strict': diagram labels are
 *  model-generated text and must not execute. */
export default function MermaidDiagram({ chart }: { chart: string }) {
  const t = { error: 'Diagram failed to render — showing the source' }
  const [svg, setSvg] = useState('')
  const [failed, setFailed] = useState(false)
  // Theme toggles rewrite attributes on <html> (theme.tsx writes data-theme);
  // watching them bumps this counter so the render effect below re-runs with
  // freshly computed themeVariables.
  const [themeTick, setThemeTick] = useState(0)
  const id = useId().replace(/[^a-zA-Z0-9]/g, '')
  const rendered = useRef('')

  useEffect(() => {
    const observer = new MutationObserver(() => setThemeTick((tick) => tick + 1))
    observer.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme', 'class'] })
    return () => observer.disconnect()
  }, [])

  useEffect(() => {
    let cancelled = false
    let key = ''
    ;(async () => {
      try {
        const mermaid = (await import('mermaid')).default
        const { darkMode, themeVariables } = mermaidTheme()
        mermaid.initialize({ startOnLoad: false, securityLevel: 'strict', theme: 'base', darkMode, themeVariables, suppressErrorRendering: true })
        let candidate = normalizeErDiagram(chart)
        let parse = await mermaid.parse(candidate, { suppressErrors: true })
        if (parse === false) {
          // Second chance: the model named a node after a word the flowchart
          // lexer reserves (graph, end, style…). Rename those ids and retry —
          // only charts that ALREADY failed reach this branch, so a valid
          // chart is never rewritten.
          const repaired = normalizeErDiagram(repairFlowchartIds(chart))
          if (repaired !== candidate) {
            candidate = repaired
            parse = await mermaid.parse(candidate, { suppressErrors: true })
          }
        }
        if (cancelled) return
        if (parse === false) { setFailed(true); return }
        key = `mermaid-${id}-${themeTick}-${candidate.length}`
        const { svg: renderedSvg } = await mermaid.render(key, candidate)
        if (cancelled) return
        rendered.current = chart
        setSvg(tuneSvgWidth(renderedSvg))
      } catch {
        // Belt and braces: remove any error container mermaid left behind.
        if (key) { document.getElementById(`d${key}`)?.remove(); document.getElementById(key)?.remove() }
        if (!cancelled) setFailed(true)
      }
    })()
    return () => { cancelled = true }
  }, [chart, id, themeTick])

  if (failed) {
    return (
      <div>
        <div style={{ fontSize: '0.68rem', color: 'var(--tm-text-3)', marginBottom: '0.15rem' }}>{t.error}</div>
        <pre style={{ padding: '0.5rem', background: 'var(--tm-raised)', color: 'var(--tm-text-2)', fontSize: '0.72rem', fontFamily: '"SFMono-Regular", Consolas, monospace', maxHeight: '260px', overflow: 'auto', borderRadius: '4px', whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>{chart}</pre>
      </div>
    )
  }

  return (
    <div
      className="mermaid-diagram"
      data-chart={rendered.current ? undefined : 'pending'}
      // mermaid.render produces a self-contained SVG; securityLevel 'strict'
      // strips scripts and event handlers from model-generated labels.
      dangerouslySetInnerHTML={{ __html: svg }}
    />
  )
}
