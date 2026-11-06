import { useMemo } from 'react'
import { useT } from './i18n'
import type { SkillManifest } from './SkillManifestEditor'
import type { SkillVersion } from './workspaceApi'

// The skill's ATTRIBUTIVE MODEL compared between versions (docs/living-skills.md):
// the manifest fields flattened to «attribute → value» rows, so the diff walks
// the MODEL — name, inputs, tools, runtime, artifacts — instead of a raw YAML
// text diff where one reordered line reads as a rewrite. The SKILL.md body
// gets the same semantic treatment through RichDiff (the artifacts' compare).

export interface ManifestAttribute {
  key: string
  value: string
}

// manifestAttributes flattens one manifest into ordered attribute rows —
// the same sections the manifest editor shows, in the same order.
export function manifestAttributes(manifest?: SkillManifest | null): ManifestAttribute[] {
  const rows: ManifestAttribute[] = []
  const push = (key: string, value: string) => rows.push({ key, value })
  const join = (list?: string[]) => (list ?? []).filter((v) => v.trim() !== '').join(' · ')
  if (!manifest) return rows
  push('name', manifest.name ?? '')
  push('description', manifest.description ?? '')
  for (const [name, field] of Object.entries(manifest.inputs ?? {})) {
    push(`inputs.${name}`, field ? [field.type, field.description, field.required ? 'required' : ''].filter(Boolean).join(' · ') : '')
  }
  for (const [name, field] of Object.entries(manifest.outputs ?? {})) {
    push(`outputs.${name}`, field ? [field.type, field.description].filter(Boolean).join(' · ') : '')
  }
  push('capabilities', join(manifest.capabilities))
  push('tools', join(manifest.tools))
  push('runtime.sandbox', manifest.runtime?.sandbox ?? '')
  push('runtime.network', manifest.runtime?.network ?? '')
  push('runtime.filesystem.read', join(manifest.runtime?.filesystem?.read))
  push('runtime.filesystem.write', join(manifest.runtime?.filesystem?.write))
  push('runtime.credentials', join(manifest.runtime?.credentials))
  push('runtime.mcp', join(manifest.runtime?.mcp))
  push('preconditions', join(manifest.preconditions))
  push('postconditions', join(manifest.postconditions))
  push('evidence.required', join(manifest.evidence?.required))
  push('evaluation.suite', manifest.evaluation?.suite ?? '')
  push('artifacts', (manifest.artifacts ?? []).map((a) => (a.note ? `${a.id} (${a.note})` : a.id)).join(' · '))
  return rows
}

export type ManifestDiffStatus = 'added' | 'removed' | 'changed' | 'same'

export interface ManifestDiffRow {
  key: string
  before: string
  after: string
  status: ManifestDiffStatus
}

// diffManifestAttributes walks the union of both models' attributes:
// an attribute missing on one side is added/removed, different values are
// changed, and rows empty on BOTH sides are noise — they never render.
export function diffManifestAttributes(before?: SkillManifest | null, after?: SkillManifest | null): ManifestDiffRow[] {
  const rows: ManifestDiffRow[] = []
  const seen = new Set<string>()
  const collect = (...lists: ManifestAttribute[][]) => {
    for (const list of lists) {
      for (const { key } of list) {
        if (!seen.has(key)) {
          seen.add(key)
          rows.push({ key, before: '', after: '', status: 'same' })
        }
      }
    }
  }
  const beforeRows = manifestAttributes(before)
  const afterRows = manifestAttributes(after)
  collect(beforeRows, afterRows)
  const valueOf = (list: ManifestAttribute[], key: string) => list.find((r) => r.key === key)?.value ?? ''
  return rows
    .map((row) => {
      const b = valueOf(beforeRows, row.key)
      const a = valueOf(afterRows, row.key)
      const status: ManifestDiffStatus = b === a ? 'same' : b === '' ? 'added' : a === '' ? 'removed' : 'changed'
      return { key: row.key, before: b, after: a, status }
    })
    .filter((row) => row.before !== '' || row.after !== '')
}

// versionLess orders skill versions numerically segment by segment
// ("1.2" < "2"), the compare-base picker's ordering.
function versionLess(a: string, b: string): boolean {
  const pa = a.split('.').map((s) => Number(s) || 0)
  const pb = b.split('.').map((s) => Number(s) || 0)
  for (let i = 0; i < Math.max(pa.length, pb.length); i++) {
    if ((pa[i] ?? 0) !== (pb[i] ?? 0)) return (pa[i] ?? 0) < (pb[i] ?? 0)
  }
  return false
}

// previousVersion picks the default compare base — the version right below
// the given one (the artifacts' semantics: compare with the PREVIOUS
// version, not with the current state).
export function previousVersion(versions: SkillVersion[], version: string): SkillVersion | undefined {
  let best: SkillVersion | undefined
  for (const v of versions) {
    if (v.version === version) continue
    if (versionLess(v.version, version) && (!best || versionLess(best.version, v.version))) best = v
  }
  return best
}

const ROW_TINT: Record<ManifestDiffStatus, string> = {
  added: 'color-mix(in srgb, var(--tm-success) 8%, transparent)',
  removed: 'color-mix(in srgb, var(--tm-danger) 8%, transparent)',
  changed: 'color-mix(in srgb, var(--tm-amber) 10%, transparent)',
  same: 'transparent',
}

/** The attributive-model compare of two skill versions: one row per
 *  attribute with both sides side by side — changed rows tinted, unchanged
 *  rows dimmed (a manifest is small enough to show in full). */
export default function SkillManifestDiff({ before, after, oldLabel, newLabel }: {
  before?: SkillManifest | null
  after?: SkillManifest | null
  oldLabel?: string
  newLabel?: string
}) {
  const t = useT()
  const rows = useMemo(() => diffManifestAttributes(before, after), [before, after])
  const counts = rows.reduce(
    (acc, row) => ({ ...acc, [row.status]: acc[row.status] + 1 }),
    { added: 0, removed: 0, changed: 0, same: 0 },
  )
  const headerStyle: React.CSSProperties = { fontSize: '0.68rem', fontWeight: 600, letterSpacing: '0.05em', textTransform: 'uppercase', color: 'var(--tm-text-3)', padding: '0.25rem 0.5rem', textAlign: 'left' }
  return (
    <div style={{ marginTop: '0.55rem' }}>
      <div style={{ fontSize: '0.68rem', fontWeight: 600, letterSpacing: '0.05em', textTransform: 'uppercase', color: 'var(--tm-text-3)', marginBottom: '0.35rem' }}>
        {t('markdown_diff.title') ?? 'What changed'} — {t('skills.attribute_model') ?? 'attribute model'}
      </div>
      <div style={{ fontSize: '0.72rem', color: 'var(--tm-text-2)', marginBottom: '0.45rem' }}>
        <span style={{ color: 'var(--tm-success)' }}>+{counts.added} {t('markdown_diff.added') ?? 'added'}</span>
        {' · '}
        <span style={{ color: 'var(--tm-amber)' }}>~{counts.changed} {t('markdown_diff.modified') ?? 'changed'}</span>
        {' · '}
        <span style={{ color: 'var(--tm-danger)' }}>−{counts.removed} {t('markdown_diff.removed') ?? 'removed'}</span>
        {' · '}
        <span>{counts.same} {t('markdown_diff.same') ?? 'unchanged'}</span>
      </div>
      {rows.length === 0 ? (
        <div style={{ fontSize: '0.75rem', color: 'var(--tm-text-3)' }}>{t('diff.no_changes') ?? 'No changes'}</div>
      ) : (
        <div style={{ border: '1px solid var(--tm-border)', borderRadius: '6px', overflow: 'hidden' }}>
          <div style={{ display: 'grid', gridTemplateColumns: 'minmax(9rem, 1.1fr) 1fr 1fr', background: 'var(--tm-raised)' }}>
            <div style={headerStyle}>{t('skills.attribute') ?? 'Attribute'}</div>
            <div style={headerStyle}>{oldLabel ? `${t('markdown_diff.before') ?? 'Before'} · ${oldLabel}` : (t('markdown_diff.before') ?? 'Before')}</div>
            <div style={headerStyle}>{newLabel ? `${t('markdown_diff.after') ?? 'After'} · ${newLabel}` : (t('markdown_diff.after') ?? 'After')}</div>
          </div>
          {rows.map((row) => (
            <div key={row.key} style={{ display: 'grid', gridTemplateColumns: 'minmax(9rem, 1.1fr) 1fr 1fr', borderTop: '1px solid var(--tm-border)', background: ROW_TINT[row.status], opacity: row.status === 'same' ? 0.55 : 1, fontSize: '0.75rem' }}>
              <div style={{ padding: '0.4rem 0.5rem', color: 'var(--tm-text-3)', fontFamily: '"SFMono-Regular", Consolas, monospace', fontSize: '0.7rem', overflowWrap: 'anywhere' }}>{row.key}</div>
              <div style={{ padding: '0.4rem 0.5rem', color: row.status === 'added' ? 'var(--tm-text-3)' : 'var(--tm-text)', whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>{row.before || '—'}</div>
              <div style={{ padding: '0.4rem 0.5rem', color: row.status === 'removed' ? 'var(--tm-text-3)' : 'var(--tm-text)', whiteSpace: 'pre-wrap', overflowWrap: 'anywhere', borderLeft: '1px solid var(--tm-border)' }}>{row.after || '—'}</div>
            </div>
          ))}
        </div>
      )}
    </div>
  )
}