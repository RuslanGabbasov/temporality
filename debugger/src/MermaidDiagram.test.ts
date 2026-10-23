import { describe, expect, it } from 'vitest'
import { normalizeErDiagram } from './MermaidDiagram'

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
