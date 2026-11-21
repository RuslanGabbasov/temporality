// @vitest-environment jsdom
import { describe, expect, it } from 'vitest'
import mermaid from 'mermaid'
import { repairFlowchartIds } from './MermaidDiagram'

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
})
