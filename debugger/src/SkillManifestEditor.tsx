import { useId, useState } from 'react'
import { TextInput, Select, SelectItem, Checkbox, TextArea, InlineNotification, Button } from '@carbon/react'
import { Add, TrashCan } from '@carbon/icons-react'
import { load as yamlLoad, dump as yamlDump } from 'js-yaml'
import { useT } from './i18n'

export interface ManifestField {
  type?: string
  description?: string
  required?: boolean
}

export interface SkillManifest {
  id?: string
  version?: string
  name?: string
  description?: string
  inputs?: Record<string, ManifestField>
  outputs?: Record<string, ManifestField>
  capabilities?: string[]
  tools?: string[]
  runtime?: {
    sandbox?: string
    network?: string
    filesystem?: { read?: string[]; write?: string[] }
    credentials?: string[]
    mcp?: string[]
  }
  preconditions?: string[]
  postconditions?: string[]
  evidence?: { required?: string[] }
  evaluation?: { suite?: string }
}

/** Known values offered as chip autosuggest, derived from the workspace. */
export interface ManifestSuggestions {
  tools?: string[]
  capabilities?: string[]
  mcp?: string[]
}

/** Where a section's current value came from. */
export type SectionProvenance = 'agent' | 'user' | 'todo'

function ProvenanceBadge({ kind }: { kind: SectionProvenance }) {
  const t = useT()
  const label = kind === 'agent' ? (t('skills.prov.agent') ?? 'from agent') : kind === 'todo' ? (t('skills.prov.todo') ?? 'needs clarification') : (t('skills.prov.user') ?? 'verified')
  return <span className={`prov-badge prov-${kind}`}>{label}</span>
}

const FIELD_TYPES = ['string', 'number', 'boolean', 'object', 'artifact']

/** Recursively drop empty strings, arrays and objects so yaml.dump stays clean. */
export function cleanManifest(value: unknown): unknown {
  if (Array.isArray(value)) {
    const items = value.map(cleanManifest).filter((v) => v !== undefined)
    return items.length ? items : undefined
  }
  if (value && typeof value === 'object') {
    const out: Record<string, unknown> = {}
    for (const [k, v] of Object.entries(value as Record<string, unknown>)) {
      if (v === false || v === null) continue
      const cleaned = cleanManifest(v)
      if (cleaned !== undefined) out[k] = cleaned
    }
    return Object.keys(out).length ? out : undefined
  }
  if (typeof value === 'string' && value.trim() === '') return undefined
  return value
}

export function manifestToYaml(manifest: SkillManifest): string {
  return yamlDump(cleanManifest(manifest) ?? {}, { lineWidth: 100, noRefs: true })
}

const sectionLabel: React.CSSProperties = {
  fontSize: '0.7rem',
  fontWeight: 600,
  letterSpacing: '0.05em',
  textTransform: 'uppercase',
  color: 'var(--tm-text-3)',
  margin: 0,
}

function AddRowButton({ label, onClick }: { label: string; onClick: () => void }) {
  return (
    <button type="button" className="add-row-button" onClick={onClick}>
      <Add size={16} />
      <span>{label}</span>
    </button>
  )
}

function RemoveButton({ label, onClick }: { label: string; onClick: () => void }) {
  return (
    <button type="button" className="remove-row-button" aria-label={label} title={label} onClick={onClick}>
      <TrashCan size={16} />
    </button>
  )
}

function ChipInput({ values, onChange, placeholder, suggestions }: {
  values: string[]
  onChange: (next: string[]) => void
  placeholder?: string
  suggestions?: string[]
}) {
  const listId = useId()
  const [draft, setDraft] = useState('')
  const commit = () => {
    const v = draft.trim()
    if (v && !values.includes(v)) onChange([...values, v])
    setDraft('')
  }
  return (
    <div className="chip-input">
      {values.map((v) => (
        <span key={v} className="chip">
          {v}
          <button type="button" aria-label={`remove ${v}`} onClick={() => onChange(values.filter((x) => x !== v))}>×</button>
        </span>
      ))}
      <input
        className="chip-field"
        list={suggestions?.length ? listId : undefined}
        value={draft}
        placeholder={placeholder}
        onChange={(change) => {
          const v = change.target.value
          if (v.endsWith(',')) {
            setDraft('')
            const item = v.slice(0, -1).trim()
            if (item && !values.includes(item)) onChange([...values, item])
          } else setDraft(v)
        }}
        onKeyDown={(change) => {
          if (change.key === 'Enter') { change.preventDefault(); commit() }
          if (change.key === 'Backspace' && !draft && values.length) onChange(values.slice(0, -1))
        }}
        onBlur={commit}
      />
      {suggestions?.length ? (
        <datalist id={listId}>
          {suggestions.filter((s) => !values.includes(s)).map((s) => <option key={s} value={s} />)}
        </datalist>
      ) : null}
    </div>
  )
}

function StringListEditor({ values, onChange, placeholder }: { values: string[]; onChange: (next: string[]) => void; placeholder?: string }) {
  const t = useT()
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: '0.4rem' }}>
      {values.map((v, i) => (
        <div key={i} className="condition-row">
          <TextInput
            id={`cond-${i}`}
            hideLabel
            labelText=""
            value={v}
            placeholder={placeholder}
            onChange={(e: React.ChangeEvent<HTMLInputElement>) => onChange(values.map((x, j) => (j === i ? e.target.value : x)))}
          />
          <RemoveButton label={t('action.delete') ?? 'Delete'} onClick={() => onChange(values.filter((_, j) => j !== i))} />
        </div>
      ))}
      <AddRowButton label={t('skills.editor.add_condition') ?? 'Add condition'} onClick={() => onChange([...values, ''])} />
    </div>
  )
}

function FieldMapEditor({ fields, onChange, addLabel, namePlaceholder }: {
  fields: Record<string, ManifestField>
  onChange: (next: Record<string, ManifestField>) => void
  addLabel: string
  namePlaceholder: string
}) {
  const t = useT()
  const typeListId = useId()
  const entries = Object.entries(fields ?? {})
  const setEntry = (name: string, patch: Partial<ManifestField>) => onChange({ ...fields, [name]: { ...fields[name], ...patch } })
  const renameEntry = (oldName: string, newName: string) => {
    if (!newName || newName === oldName) return
    const next: Record<string, ManifestField> = {}
    for (const [k, v] of entries) next[k === oldName ? newName : k] = v
    onChange(next)
  }
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: '0.5rem' }}>
      <datalist id={typeListId}>{FIELD_TYPES.map((ft) => <option key={ft} value={ft} />)}</datalist>
      {entries.map(([name, field]) => (
        <div key={name} className="manifest-field">
          <div className="manifest-field-grid">
            <TextInput
              id={`field-name-${name}`}
              hideLabel
              labelText="name"
              value={name}
              placeholder={namePlaceholder}
              onChange={(e: React.ChangeEvent<HTMLInputElement>) => renameEntry(name, e.target.value)}
            />
            <TextInput
              id={`field-type-${name}`}
              hideLabel
              labelText="type"
              value={field.type ?? ''}
              placeholder="string"
              list={typeListId}
              onChange={(e: React.ChangeEvent<HTMLInputElement>) => setEntry(name, { type: e.target.value })}
            />
            <Checkbox
              id={`field-required-${name}`}
              labelText={t('skills.editor.field_required') ?? 'Required'}
              checked={Boolean(field.required)}
              onChange={(_e: React.ChangeEvent<HTMLInputElement>, data: { checked: boolean }) => setEntry(name, { required: data.checked })}
              title={t('skills.editor.field_required') ?? 'Required'}
            />
            <RemoveButton label={t('action.delete') ?? 'Delete'} onClick={() => { const next = { ...fields }; delete next[name]; onChange(next) }} />
          </div>
          <TextInput
            id={`field-desc-${name}`}
            hideLabel
            labelText="description"
            value={field.description ?? ''}
            placeholder={t('skills.editor.field_description') ?? 'Description'}
            onChange={(e: React.ChangeEvent<HTMLInputElement>) => setEntry(name, { description: e.target.value })}
            style={{ width: '100%', boxSizing: 'border-box' }}
          />
        </div>
      ))}
      <AddRowButton label={addLabel} onClick={() => onChange({ ...fields, '': { type: 'string' } })} />
    </div>
  )
}

/**
 * Structured editor for skill.yaml — form controls for the formal manifest
 * sections, with a raw-YAML escape hatch for power users.
 */
export default function SkillManifestEditor({ manifest, onChange, suggestions, provenance, onTouch }: {
  manifest: SkillManifest
  onChange: (next: SkillManifest) => void
  suggestions?: ManifestSuggestions
  provenance?: Record<string, SectionProvenance>
  onTouch?: (section: string) => void
}) {
  const t = useT()
  const [rawMode, setRawMode] = useState(false)
  const [rawText, setRawText] = useState('')
  const [rawError, setRawError] = useState('')

  const patch = (part: Partial<SkillManifest>) => onChange({ ...manifest, ...part })
  const touch = (section: string) => onTouch?.(section)
  const badge = (section: string) => provenance?.[section]
  const SectionLabel = ({ id, children }: { id: string; children: React.ReactNode }) => (
    <div className="manifest-section-head">
      <div style={sectionLabel}>{children}</div>
      {badge(id) && <ProvenanceBadge kind={badge(id)!} />}
    </div>
  )
  const runtime = manifest.runtime ?? {}
  const patchRuntime = (part: Partial<NonNullable<SkillManifest['runtime']>>) => { touch('runtime'); patch({ runtime: { ...runtime, ...part } }) }

  const enterRaw = () => { setRawText(manifestToYaml(manifest)); setRawError(''); setRawMode(true) }
  const leaveRaw = () => {
    try {
      const parsed = yamlLoad(rawText)
      onChange((parsed && typeof parsed === 'object' ? parsed : {}) as SkillManifest)
      onTouch?.('*')
      setRawMode(false)
    } catch (err) {
      setRawError(err instanceof Error ? err.message : 'Invalid YAML')
    }
  }

  if (rawMode) {
    return (
      <div className="manifest-editor">
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '0.4rem' }}>
          <h3 style={sectionLabel}>skill.yaml</h3>
          <Button kind="ghost" size="sm" onClick={leaveRaw}>{t('skills.editor.structured') ?? 'Back to form'}</Button>
        </div>
        {rawError && <InlineNotification kind="error" title={t('skills.editor.yaml_error') ?? 'Invalid YAML'} subtitle={rawError} lowContrast hideCloseButton style={{ marginBottom: '0.4rem' }} />}
        <TextArea
          id="skill-manifest-raw"
          hideLabel
          labelText="skill.yaml"
          rows={14}
          value={rawText}
          onChange={(e: React.ChangeEvent<HTMLTextAreaElement>) => { setRawText(e.target.value); setRawError('') }}
          style={{ fontFamily: 'var(--tm-font-mono, monospace)', fontSize: '0.8rem' }}
        />
      </div>
    )
  }

  return (
    <div className="manifest-editor">
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
        <h3 style={sectionLabel}>{t('skills.editor.contract') ?? 'Contract'}</h3>
        <Button kind="ghost" size="sm" onClick={enterRaw}>{t('skills.editor.raw_yaml') ?? 'Edit as YAML'}</Button>
      </div>

      <div className="manifest-section">
        <SectionLabel id="inputs">{t('skills.editor.inputs') ?? 'Inputs'}</SectionLabel>
        <FieldMapEditor fields={manifest.inputs ?? {}} onChange={(inputs) => { touch('inputs'); patch({ inputs }) }} addLabel={t('skills.editor.add_input') ?? 'Add input'} namePlaceholder="repository" />
      </div>

      <div className="manifest-section">
        <SectionLabel id="outputs">{t('skills.editor.outputs') ?? 'Outputs'}</SectionLabel>
        <FieldMapEditor fields={manifest.outputs ?? {}} onChange={(outputs) => { touch('outputs'); patch({ outputs }) }} addLabel={t('skills.editor.add_output') ?? 'Add output'} namePlaceholder="deployment" />
      </div>

      <div className="manifest-section">
        <SectionLabel id="capabilities">{t('skills.editor.capabilities') ?? 'Capabilities'}</SectionLabel>
        <ChipInput values={manifest.capabilities ?? []} onChange={(capabilities) => { touch('capabilities'); patch({ capabilities }) }} placeholder={t('skills.editor.chip_hint') ?? 'Type and press Enter'} suggestions={suggestions?.capabilities} />
      </div>

      <div className="manifest-section">
        <SectionLabel id="tools">{t('skills.editor.tools') ?? 'Tools'}</SectionLabel>
        <ChipInput values={manifest.tools ?? []} onChange={(tools) => { touch('tools'); patch({ tools }) }} placeholder={t('skills.editor.chip_hint') ?? 'Type and press Enter'} suggestions={suggestions?.tools} />
      </div>

      <div className="manifest-section">
        <SectionLabel id="runtime">{t('skills.editor.runtime') ?? 'Runtime'}</SectionLabel>
        <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: '0.75rem' }}>
          <Select id="runtime-sandbox" labelText={t('skills.editor.sandbox') ?? 'Sandbox'} value={runtime.sandbox ?? ''} onChange={(e: React.ChangeEvent<HTMLSelectElement>) => patchRuntime({ sandbox: e.target.value })}>
            <SelectItem value="" text="—" />
            <SelectItem value="required" text="required" />
            <SelectItem value="optional" text="optional" />
            <SelectItem value="none" text="none" />
          </Select>
          <Select id="runtime-network" labelText={t('skills.editor.network') ?? 'Network'} value={runtime.network ?? ''} onChange={(e: React.ChangeEvent<HTMLSelectElement>) => patchRuntime({ network: e.target.value })}>
            <SelectItem value="" text="—" />
            <SelectItem value="restricted" text="restricted" />
            <SelectItem value="open" text="open" />
            <SelectItem value="none" text="none" />
          </Select>
        </div>
        <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: '0.75rem', marginTop: '0.75rem' }}>
          <div>
            <div style={sectionLabel}>{t('skills.editor.fs_read') ?? 'Filesystem read'}</div>
            <ChipInput values={runtime.filesystem?.read ?? []} onChange={(read) => patchRuntime({ filesystem: { ...runtime.filesystem, read } })} placeholder="/etc, ./repo" />
          </div>
          <div>
            <div style={sectionLabel}>{t('skills.editor.fs_write') ?? 'Filesystem write'}</div>
            <ChipInput values={runtime.filesystem?.write ?? []} onChange={(write) => patchRuntime({ filesystem: { ...runtime.filesystem, write } })} placeholder="/tmp, ./dist" />
          </div>
        </div>
        <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: '0.75rem', marginTop: '0.75rem' }}>
          <div>
            <div style={sectionLabel}>{t('skills.editor.credentials') ?? 'Credentials'}</div>
            <ChipInput values={runtime.credentials ?? []} onChange={(credentials) => patchRuntime({ credentials })} placeholder="registry-token" />
          </div>
          <div>
            <div style={sectionLabel}>{t('skills.editor.mcp') ?? 'MCP servers'}</div>
            <ChipInput values={runtime.mcp ?? []} onChange={(mcp) => patchRuntime({ mcp })} placeholder="tracker" suggestions={suggestions?.mcp} />
          </div>
        </div>
      </div>

      <div className="manifest-section">
        <SectionLabel id="preconditions">{t('skills.editor.preconditions') ?? 'Preconditions'}</SectionLabel>
        <StringListEditor values={manifest.preconditions ?? []} onChange={(preconditions) => { touch('preconditions'); patch({ preconditions }) }} placeholder={t('skills.editor.condition_placeholder') ?? 'Describe a condition'} />
      </div>

      <div className="manifest-section">
        <SectionLabel id="postconditions">{t('skills.editor.postconditions') ?? 'Postconditions'}</SectionLabel>
        <StringListEditor values={manifest.postconditions ?? []} onChange={(postconditions) => { touch('postconditions'); patch({ postconditions }) }} placeholder={t('skills.editor.condition_placeholder') ?? 'Describe a condition'} />
      </div>

      <div className="manifest-section">
        <SectionLabel id="evidence">{t('skills.editor.evidence') ?? 'Evidence required'}</SectionLabel>
        <ChipInput values={manifest.evidence?.required ?? []} onChange={(required) => { touch('evidence'); patch({ evidence: { required } }) }} placeholder={t('skills.editor.chip_hint') ?? 'Type and press Enter'} />
      </div>

      <div className="manifest-section">
        <SectionLabel id="evaluation">{t('skills.editor.evaluation') ?? 'Evaluation suite'}</SectionLabel>
        <TextInput
          id="evaluation-suite"
          labelText={t('skills.editor.evaluation') ?? 'Evaluation suite'}
          value={manifest.evaluation?.suite ?? ''}
          placeholder="deploy-service/default"
          onChange={(e: React.ChangeEvent<HTMLInputElement>) => { touch('evaluation'); patch({ evaluation: { suite: e.target.value } }) }}
        />
      </div>
    </div>
  )
}
