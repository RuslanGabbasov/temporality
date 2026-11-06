import { describe, expect, it } from 'vitest'
import { manifestAttributes, diffManifestAttributes, previousVersion } from './SkillManifestDiff'
import type { SkillManifest } from './SkillManifestEditor'
import type { SkillVersion } from './workspaceApi'

const before: SkillManifest = {
  name: 'Word processing',
  description: 'Работа с документами',
  inputs: { file: { type: 'artifact', required: true } },
  tools: ['read_file', 'write_file'],
  runtime: { sandbox: 'polyglot-dev-box', network: 'deny', filesystem: { read: ['/workspace'], write: ['/scratch'] } },
  artifacts: [{ id: 'doc-1', note: 'спека' }],
}

const after: SkillManifest = {
  name: 'Word processing',
  description: 'Работа с документами и отчётами',
  inputs: { file: { type: 'artifact', required: true }, template: { type: 'artifact' } },
  tools: ['read_file', 'write_file', 'artifact_read'],
  runtime: { sandbox: 'polyglot-dev-box', network: 'deny', filesystem: { read: ['/workspace'], write: ['/scratch'] } },
  artifacts: [{ id: 'doc-1', note: 'спека' }],
}

describe('manifestAttributes', () => {
  it('flattens the attributive model into comparable rows', () => {
    const rows = manifestAttributes(before)
    const byKey = new Map(rows.map((r) => [r.key, r.value]))
    expect(byKey.get('name')).toBe('Word processing')
    expect(byKey.get('inputs.file')).toContain('required')
    expect(byKey.get('tools')).toBe('read_file · write_file')
    expect(byKey.get('runtime.filesystem.write')).toBe('/scratch')
    expect(byKey.get('artifacts')).toBe('doc-1 (спека)')
  })
})

describe('diffManifestAttributes', () => {
  it('walks the model, not YAML lines', () => {
    const rows = diffManifestAttributes(before, after)
    const byKey = new Map(rows.map((r) => [r.key, r]))
    expect(byKey.get('description')?.status).toBe('changed')
    expect(byKey.get('inputs.template')?.status).toBe('added')
    expect(byKey.get('tools')?.status).toBe('changed')
    expect(byKey.get('tools')?.after).toContain('artifact_read')
    expect(byKey.get('runtime.sandbox')?.status).toBe('same')
    expect(byKey.get('name')?.status).toBe('same')
  })

  it('attributes empty on both sides never render', () => {
    const rows = diffManifestAttributes({ name: 'a' }, { name: 'a' })
    expect(rows.map((r) => r.key)).toEqual(['name'])
  })

  it('removed attributes say so', () => {
    const rows = diffManifestAttributes({ name: 'a', evaluation: { suite: 'smoke' } }, { name: 'a' })
    const row = rows.find((r) => r.key === 'evaluation.suite')
    expect(row?.status).toBe('removed')
    expect(row?.before).toBe('smoke')
    expect(row?.after).toBe('')
  })
})

describe('previousVersion', () => {
  const versions = (['1', '1.1', '2', '10'] as string[]).map((version) => ({ skill_id: 's', version, markdown: '', manifest: {}, created_at: '' })) as SkillVersion[]
  it('picks the version right below — the artifacts compare default', () => {
    expect(previousVersion(versions, '2')?.version).toBe('1.1')
    expect(previousVersion(versions, '10')?.version).toBe('2')
    expect(previousVersion(versions, '1')).toBeUndefined()
  })
})
