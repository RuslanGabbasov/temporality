import { useState } from 'react'
import { Button, InlineNotification, Tag, TextArea, TextInput, Tile } from '@carbon/react'
import { ChevronRight } from '@carbon/icons-react'
import { observationApi, type ObservationHint } from '../observationApi'
import { stateLabel } from './shared'
import { useT } from '../i18n'

/** Priming preview moved from the old Observability knowledge tab (stage 4):
 *  the same /hints endpoint the kernel uses before a tool call — check what
 *  memory the matcher would surface for a query/tool-result pair. Collapsed
 *  by default; it is a diagnostics tool, not part of the daily flow. */
export default function HintsPreview({ project }: { project: string }) {
  const t = useT()
  const [open, setOpen] = useState(false)
  const [query, setQuery] = useState('')
  const [tool, setTool] = useState('')
  const [toolResult, setToolResult] = useState('')
  const [hints, setHints] = useState<ObservationHint[]>([])
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  async function retrieve() {
    if (!query.trim() && !toolResult.trim()) return
    setBusy(true); setError('')
    try {
      const response = await observationApi.hints({ project, query, tool: tool || undefined, tool_result: toolResult, limit: 7 })
      setHints(response.hints ?? [])
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Request failed')
    } finally {
      setBusy(false)
    }
  }

  return (
    <div style={{ border: '1px solid var(--tm-border)', borderRadius: 'var(--tm-radius-md)', marginBottom: '0.75rem' }}>
      <button
        onClick={() => setOpen((value) => !value)}
        style={{
          width: '100%', display: 'flex', alignItems: 'center', gap: '0.4rem',
          padding: '0.55rem 0.75rem', background: 'transparent', border: 'none',
          color: open ? 'var(--tm-teal)' : 'var(--tm-text-2)', cursor: 'pointer',
          fontSize: '0.78rem', fontWeight: 600,
        }}
      >
        <span className="chevron-rotate" style={{ display: 'inline-flex', ...(open ? { transform: 'rotate(90deg)' } : {}) }}><ChevronRight size={14} /></span>
        {t('explorer.hints_title') ?? 'Priming preview'}
      </button>
      {open && (
        <div style={{ padding: '0 0.75rem 0.75rem' }}>
          <p style={{ fontSize: '0.72rem', color: 'var(--tm-muted)', margin: '0 0 0.6rem' }}>
            {t('explorer.hints_hint') ?? ''}
          </p>
          <div style={{ display: 'grid', gap: '0.5rem' }}>
            <TextArea id="hints-query" labelText={t('knowledge.query') ?? 'Query'} value={query} onChange={(e) => setQuery(e.target.value)} rows={2} placeholder="What is the agent trying to do?" />
            <TextInput id="hints-tool" labelText={t('knowledge.tool') ?? 'Tool'} value={tool} onChange={(e) => setTool(e.target.value)} placeholder="test-runner" />
            <TextArea id="hints-tool-result" labelText={t('knowledge.tool_result') ?? 'Tool result'} value={toolResult} onChange={(e) => setToolResult(e.target.value)} rows={2} placeholder="Relevant excerpt" />
            <div>
              <Button size="sm" onClick={retrieve} disabled={busy || (!query.trim() && !toolResult.trim())}>
                {busy ? (t('knowledge.searching') ?? 'Searching…') : (t('knowledge.retrieve_hints') ?? 'Retrieve hints')}
              </Button>
            </div>
            {error && <InlineNotification kind="error" title="Error" subtitle={error} onClose={() => setError('')} lowContrast />}
            {hints.map((hint) => (
              <Tile key={hint.hint_id} style={{ padding: '0.5rem' }}>
                <Tag type="gray" size="sm">{stateLabel(hint.state, t)}</Tag>
                <strong style={{ fontSize: '0.85rem' }}>{hint.proposition}</strong>
                {hint.caution && <small style={{ color: 'var(--tm-warning)', display: 'block', marginTop: '0.25rem' }}>{hint.caution}</small>}
                <small style={{ color: 'var(--tm-muted)', display: 'block' }}>{t('knowledge.matched_by') ?? 'Matched by'}: {hint.matched_by.join(', ')}</small>
              </Tile>
            ))}
          </div>
        </div>
      )}
    </div>
  )
}
