// @vitest-environment jsdom
import { beforeAll, describe, expect, it } from 'vitest'
import mermaid from 'mermaid'
import { repairFlowchartIds, tuneSvgWidth } from './MermaidDiagram'

// jsdom has no SVG layout engine; mermaid only needs the measurement hooks
// to return plausible boxes.
beforeAll(() => {
  const box = { x: 0, y: 0, width: 320, height: 120 }
  // @ts-expect-error jsdom missing API
  window.SVGElement.prototype.getBBox = () => ({ ...box })
  // @ts-expect-error jsdom missing API
  window.SVGElement.prototype.getComputedTextLength = () => 320
})

// End-to-end guard for the reserved-id repair: the EXACT chart a model
// emitted (node named `graph`) must go from parse-failure to parse-success
// after repair — and valid structures must keep parsing untouched.

const reported = `flowchart TB
    sap["SAP × TechWolf (окт. 2026)"]
    graph["Граф работы: tasks → skills → jobs"]
    agent["Grounding-слой для AI-агентов"]
    hcm["Skills-слой = ядро HCM"]
    hrpro["У HR Pro уже есть: оргструктура + RX + Projects + Omni"]
    win{"Окно в РФ: 2–3 года"}
    sap --> graph
    graph --> agent
    agent --> hcm
    hcm -->|"наш актив"| hrpro
    hrpro --> win`

async function parses(source: string): Promise<boolean> {
  try {
    // Success resolves to { diagramType, config } — truthy, not `true`;
    // failure throws (parse runs without suppressErrors here).
    await mermaid.parse(source)
    return true
  } catch {
    return false
  }
}

describe('flowchart reserved-id repair (integration)', () => {
  it('pins the bug: the reported chart fails to parse as emitted', async () => {
    mermaid.initialize({ startOnLoad: false, securityLevel: 'strict', theme: 'base' })
    expect(await parses(reported)).toBe(false)
  })

  it('renders the reported chart after repair', async () => {
    expect(await parses(repairFlowchartIds(reported))).toBe(true)
  })

  it('keeps valid structures parsing after repair', async () => {
    const valid = `flowchart TB
    subgraph s
      a[graph]
      a["graph"] --> b
    end
    style a fill:#fff,stroke:#333
    classDef warn color:red
    class a warn
    a -->|graph| b
    b --> c`
    expect(await parses(valid)).toBe(true)
    expect(await parses(repairFlowchartIds(valid))).toBe(true)
    expect(repairFlowchartIds(valid)).toBe(valid)
  })

  // The width complaint: mermaid's useMaxWidth path caps the root svg with
  // an inline `max-width: Npx` — the tune converts it to a fixed width so
  // the CSS min-width can stretch narrow diagrams to the text column.
  it('converts the rendered natural-width cap into a fixed width', async () => {
    mermaid.initialize({ startOnLoad: false, securityLevel: 'strict', theme: 'base' })
    const { svg } = await mermaid.render('tune-width', 'flowchart TB\n    a[Заголовок] --> b[Цель]')
    const tuned = tuneSvgWidth(svg)
    expect(tuned.startsWith('<svg')).toBe(true)
    // The cap lives on the ROOT svg tag; `max-width:` elsewhere (mermaid's
    // injected CSS, html-label widths) must stay untouched.
    const rootTag = (source: string) => source.slice(0, source.indexOf('>') + 1)
    if (rootTag(svg).includes('max-width:')) {
      expect(rootTag(tuned)).not.toContain('max-width:')
      expect(rootTag(tuned)).toContain('width:')
    } else {
      // useMaxWidth=false build: the natural width lives in the attribute —
      // untouched here, the CSS min-width handles the column fill.
      expect(tuned).toBe(svg)
    }
    // The tuned svg must still be the same document (no truncated tags).
    expect(tuned.match(/<svg/g)?.length).toBe(svg.match(/<svg/g)?.length)
    expect(tuned.match(/<\/svg>/g)?.length).toBe(svg.match(/<\/svg>/g)?.length)
  })
})
