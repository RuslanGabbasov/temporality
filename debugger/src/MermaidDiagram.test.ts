import { describe, expect, it } from 'vitest'
import { normalizeErDiagram, readableOnFill, repairFlowchartIds, tuneSvgWidth } from './MermaidDiagram'

describe('normalizeErDiagram', () => {
  it('passes a valid diagram through unchanged', () => {
    const valid = [
      'erDiagram',
      '    RUN ||--o{ USER : contains',
      '    RUN {',
      '        string run_id PK "The id"',
      '        int count',
      '    }',
    ].join('\n')
    expect(normalizeErDiagram(valid)).toBe(valid)
  })

  it('ignores non-erDiagram charts entirely', () => {
    const flow = 'flowchart TD\n    A --> B'
    expect(normalizeErDiagram(flow)).toBe(flow)
  })

  it('passes crow\u0027s-foot relationship lines through untouched', () => {
    const normalized = normalizeErDiagram([
      'erDiagram',
      '    RUN ||--o{ TARGET_USER : contains',
      '    TARGET_ROOM }|--|| RUN : belongs',
      '    A |o..o| B : links',
    ].join('\n'))
    expect(normalized).toContain('RUN ||--o{ TARGET_USER : contains')
    expect(normalized).toContain('TARGET_ROOM }|--|| RUN : belongs')
    expect(normalized).toContain('A |o..o| B : links')
  })

  it('expands several names sharing one type onto one line each', () => {
    const normalized = normalizeErDiagram([
      'erDiagram',
      '    RUN {',
      '        string started_at finished_at operator',
      '    }',
    ].join('\n'))
    expect(normalized).toContain('string started_at')
    expect(normalized).toContain('string finished_at')
    expect(normalized).toContain('string operator')
    expect(normalized).not.toContain('started_at finished_at')
  })

  it('splits ;-joined inline entity bodies into one attribute per line', () => {
    const normalized = normalizeErDiagram('erDiagram\n    ROOM_DELETION_TASK { string delete_id room_id status error; int attempts }')
    expect(normalized).toContain('ROOM_DELETION_TASK {')
    expect(normalized).toContain('string delete_id')
    expect(normalized).toContain('string room_id')
    expect(normalized).toContain('string status')
    expect(normalized).toContain('string error')
    expect(normalized).toContain('int attempts')
    expect(normalized).toContain('}')
    expect(normalized).not.toContain(';')
  })

  it('leaves key markers and quoted comments alone', () => {
    const normalized = normalizeErDiagram([
      'erDiagram',
      '    RUN {',
      '        string id PK "The identifier"',
      '        string phase "snapshot|reports"',
      '        string ref FK',
      '    }',
    ].join('\n'))
    expect(normalized).toContain('string id PK "The identifier"')
    expect(normalized).toContain('string phase "snapshot|reports"')
    expect(normalized).toContain('string ref FK')
  })

  it('normalizes the real artifact diagram that crashed the renderer', () => {
    // The exact erDiagram from run ne-sobiraetsya-v-ci: ;-joined inline
    // entities and packed attribute lines — every known shortcut at once.
    const chart = [
      'erDiagram',
      '    RUN ||--o{ TARGET_USER : contains',
      '    RUN ||--o{ TARGET_ROOM : contains',
      '    RUN ||--o{ VERIFICATION_CHECK : produces',
      '    TARGET_ROOM ||--o{ ROOM_DELETION_TASK : attempts',
      '    TARGET_ROOM ||--o{ EVENT_REPORT_REF : reports',
      '    RUN {',
      '        string run_id UUID',
      '        string tenant_id',
      '        string tool_version',
      '        string synapse_version_target',
      '        string phase "snapshot|reports|media|purge|users|verify"',
      '        string status "planned|running|partial|done|failed|aborted"',
      '        string started_at finished_at operator',
      '    }',
      '    TARGET_USER {',
      '        string mxid',
      '        string ids_username ids_owner_id',
      '        string state "enumerated|media_deleted|ratelimit_cleared|deactivated|erased|verified|absent_in_synapse|no_mxid|failed"',
      '        int media_deleted',
      '    }',
      '    TARGET_ROOM {',
      '        string room_id',
      '        string discovered_via "memberships|creator_scan"',
      '        bool invariant_ok',
      '        string state "enumerated|invariant_failed|already_purged|purge_scheduled|purge_active|purge_complete|purged_verified|failed"',
      '        string delete_id',
      '    }',
      '    ROOM_DELETION_TASK { string delete_id room_id status error; int attempts }',
      '    EVENT_REPORT_REF { int report_id; string room_id user_id state }',
      '    VERIFICATION_CHECK { string check_id target expected observed; bool passed }',
    ].join('\n')
    const normalized = normalizeErDiagram(chart)
    // Packed lines are gone; every attribute sits on its own `type name` line.
    expect(normalized).not.toContain('started_at finished_at')
    expect(normalized).not.toContain(';')
    expect(normalized).toContain('string check_id')
    expect(normalized).toContain('string target')
    expect(normalized).toContain('string expected')
    expect(normalized).toContain('string observed')
    expect(normalized).toContain('bool passed')
    expect(normalized).toContain('int report_id')
    // Quoted range-comments survive verbatim.
    expect(normalized).toContain('"snapshot|reports|media|purge|users|verify"')
  })
})

describe('readableOnFill', () => {
  it('picks dark text on light brand fills and light text on dark ones', () => {
    // The mindmap ramp labels sit ON the chart palette: the yellow band is
    // light and needs dark text; the blue/purple bands need light text.
    expect(readableOnFill('#f1c21b')).toBe('#14201D')
    expect(readableOnFill('#d09900')).toBe('#FBF8F2')
    expect(readableOnFill('#4589ff')).toBe('#FBF8F2')
    expect(readableOnFill('#24a148')).toBe('#FBF8F2')
  })

  it('falls back to light text for non-hex values', () => {
    expect(readableOnFill('rgba(1,2,3,0.5)')).toBe('#FBF8F2')
    expect(readableOnFill('')).toBe('#FBF8F2')
  })
})

// A model naming a node `graph` used to kill the whole diagram: the
// flowchart lexer turns the reserved word into a GRAPH keyword token where
// it expects a node id («got 'GRAPH'»). The repair renames reserved ids —
// and must leave every valid structure byte-identical.
describe('repairFlowchartIds', () => {
  const reported = [
    'flowchart TB',
    '    sap["SAP × TechWolf (окт. 2026)"]',
    '    graph["Граф работы: tasks → skills → jobs"]',
    '    agent["Grounding-слой для AI-агентов"]',
    '    hcm["Skills-слой = ядро HCM"]',
    '    hrpro["У HR Pro уже есть: оргструктура + RX + Projects + Omni"]',
    '    win{"Окно в РФ: 2–3 года"}',
    '    sap --> graph',
    '    graph --> agent',
    '    agent --> hcm',
    '    hcm -->|"наш актив"| hrpro',
    '    hrpro --> win',
  ].join('\n')

  it('renames the reserved id in declarations and edges, labels untouched', () => {
    const repaired = repairFlowchartIds(reported)
    expect(repaired).toContain('graph_["Граф работы: tasks → skills → jobs"]')
    expect(repaired).toContain('sap --> graph_')
    expect(repaired).toContain('graph_ --> agent')
    // Quoted labels keep their text — only bare ids are rewritten.
    expect(repaired).toContain('"Граф работы: tasks → skills → jobs"')
    expect(repaired).toContain('hcm -->|"наш актив"| hrpro')
  })

  it('leaves valid structures byte-identical', () => {
    const valid = [
      'flowchart TB',
      '    subgraph s',
      '      a[graph]',
      '      a["graph"] --> b',
      '    end',
      '    style a fill:#fff,stroke:#333',
      '    classDef warn color:red',
      '    class a warn',
      '    click a href "https://x"',
      '    a -->|graph| b',
      '    %% graph comment',
      '    b --> end',
    ].join('\n')
    const repaired = repairFlowchartIds(valid)
    // Everything survives except the reserved id in the edge target.
    expect(repaired).toContain('subgraph s')
    expect(repaired).toContain('    end')
    expect(repaired).toContain('a[graph]')
    expect(repaired).toContain('a["graph"] --> b')
    expect(repaired).toContain('style a fill:#fff,stroke:#333')
    expect(repaired).toContain('classDef warn color:red')
    expect(repaired).toContain('class a warn')
    expect(repaired).toContain('click a href "https://x"')
    expect(repaired).toContain('a -->|graph| b')
    expect(repaired).toContain('%% graph comment')
    expect(repaired).toContain('b --> end_')
  })

  it('keeps the diagram-type line and non-flowchart charts untouched', () => {
    expect(repairFlowchartIds('graph TB\n    a --> b')).toBe('graph TB\n    a --> b')
    expect(repairFlowchartIds('flowchart TB\n    a --> b')).toBe('flowchart TB\n    a --> b')
    const er = 'erDiagram\n    RUN ||--o{ USER : contains'
    expect(repairFlowchartIds(er)).toBe(er)
  })

  it('renames reserved ids in statement positions and subgraph headers', () => {
    expect(repairFlowchartIds('flowchart TB\n    style graph fill:#eee')).toBe('flowchart TB\n    style graph_ fill:#eee')
    expect(repairFlowchartIds('flowchart TB\n    subgraph graph')).toBe('flowchart TB\n    subgraph graph_')
    expect(repairFlowchartIds('flowchart TB\n    end["x"]')).toBe('flowchart TB\n    end_["x"]')
  })
})

// mermaid's useMaxWidth path caps the svg at its natural width with an
// INLINE style (`max-width: Npx`) that no stylesheet outranks — diagrams
// narrower than the text column never filled it. The tune converts the cap
// into a fixed width so the CSS min-width can stretch narrow drawings to
// the column while wide ones keep N and scroll.
describe('tuneSvgWidth', () => {
  it('converts the inline natural-width cap into a fixed width', () => {
    const svg = '<svg id="x" width="100%" style="max-width: 640px;" viewBox="0 0 640 400"></svg>'
    expect(tuneSvgWidth(svg)).toBe('<svg id="x" width="100%" style="width: 640px;" viewBox="0 0 640 400"></svg>')
  })

  it('keeps other style declarations around the cap', () => {
    const svg = '<svg style="color:red;max-width: 12.5px"></svg>'
    expect(tuneSvgWidth(svg)).toBe('<svg style="color:red;width: 12.5px"></svg>')
  })

  it('leaves svgs without a cap (useMaxWidth=false) untouched', () => {
    const svg = '<svg width="640" height="400" viewBox="0 0 640 400"></svg>'
    expect(tuneSvgWidth(svg)).toBe(svg)
    expect(tuneSvgWidth('<svg style="color:red"></svg>')).toBe('<svg style="color:red"></svg>')
  })

  it('only rewrites the root svg tag', () => {
    const svg = '<svg style="max-width: 10px"><g><svg style="max-width: 99px"></svg></g></svg>'
    expect(tuneSvgWidth(svg)).toBe('<svg style="width: 10px"><g><svg style="max-width: 99px"></svg></g></svg>')
  })
})
