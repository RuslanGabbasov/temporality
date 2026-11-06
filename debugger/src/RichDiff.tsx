import { useMemo, useState, type ReactNode } from 'react'
import Markdown from './Markdown'
import { diffText } from './textDiff'
import { useI18n, pluralT } from './i18n'
import { diffMarkdown, type DiffRow, type WordChange } from './markdownDiff'

// Strategies share a result model and UI; source formats preserve text literally.
const strategies = {
  markdown: { compare: diffMarkdown, Content: Markdown },
  yaml: { compare: diffText, Content: SourceText },
  text: { compare: diffText, Content: SourceText },
}

function SourceText({ content }: { content: string }) {
  return <pre style={{ margin: 0, whiteSpace: 'pre-wrap', overflowWrap: 'anywhere', fontFamily: 'var(--tm-mono)' }}><code>{content}</code></pre>
}

function WordStrip({ changes }: { changes: WordChange[] }) {
  return (
    <p style={{ fontSize: '0.75rem', lineHeight: 1.7, marginTop: '0.4rem', wordBreak: 'break-word' }}>
      {changes.map((change, i) => {
        if (change.op === 'add') {
          return <span key={i} style={{ background: 'color-mix(in srgb, var(--tm-success) 22%, transparent)', color: 'var(--tm-text)', borderRadius: 3, padding: '0 2px' }}>{change.text}</span>
        }
        if (change.op === 'del') {
          return <span key={i} style={{ background: 'color-mix(in srgb, var(--tm-danger) 20%, transparent)', color: 'var(--tm-text-2)', textDecoration: 'line-through', borderRadius: 3, padding: '0 2px' }}>{change.text}</span>
        }
        return <span key={i} style={{ color: 'var(--tm-text-3)' }}>{change.text}</span>
      })}
    </p>
  )
}

function BlockPanel({ label, text, tone, format }: { label: string; text: string; tone: 'add' | 'del' | 'plain'; format: NonNullable<RichDiffProps['format']> }) {
  const Content = strategies[format].Content
  const border = tone === 'add' ? 'var(--tm-success)' : tone === 'del' ? 'var(--tm-danger)' : 'var(--tm-border)'
  const wash = tone === 'add'
    ? 'color-mix(in srgb, var(--tm-success) 8%, transparent)'
    : tone === 'del'
      ? 'color-mix(in srgb, var(--tm-danger) 8%, transparent)'
      : 'transparent'
  return (
    <div style={{ flex: 1, minWidth: '240px', border: '1px solid var(--tm-border)', borderLeft: `3px solid ${border}`, borderRadius: '6px', padding: '0.55rem 0.7rem', background: wash }}>
      <div style={{ fontSize: '0.68rem', fontWeight: 600, letterSpacing: '0.05em', textTransform: 'uppercase', color: 'var(--tm-text-3)', marginBottom: '0.35rem' }}>{label}</div>
      <div className="artifact-body" style={{ fontSize: '0.82rem' }}><Content content={text} /></div>
    </div>
  )
}

interface RichDiffProps {
  oldText: string
  newText: string
  oldLabel?: string
  newLabel?: string
  /** Structured text uses exact line comparison, preserving indentation and case. */
  format?: 'markdown' | 'yaml' | 'text'
  title?: string
}

export default function RichDiff({ oldText, newText, oldLabel, newLabel, title, format = 'markdown' }: RichDiffProps) {
  const { t, locale } = useI18n()
  const { compare, Content } = strategies[format]
  const diff = useMemo(() => compare(oldText ?? '', newText ?? ''), [compare, oldText, newText])
  const [showSame, setShowSame] = useState(false)

  const beforeLabel = oldLabel ?? (t('markdown_diff.before') ?? 'Before')
  const afterLabel = newLabel ?? (t('markdown_diff.after') ?? 'After')

  const rendered: ReactNode[] = []
  let sameRun: DiffRow[] = []
  const flushSame = () => {
    if (sameRun.length === 0) return
    const run = sameRun
    sameRun = []
    rendered.push(
      <div key={`same-${rendered.length}`} style={{ margin: '0.45rem 0' }}>
        <button
          type="button"
          onClick={() => setShowSame((v) => !v)}
          style={{ background: 'none', border: '1px dashed var(--tm-border)', borderRadius: '6px', color: 'var(--tm-text-3)', cursor: 'pointer', fontSize: '0.72rem', padding: '0.3rem 0.7rem' }}
        >
          {showSame ? '▴' : '▾'} {pluralT(t, locale, 'markdown_diff.same_n', run.length)}
        </button>
        {showSame && (
          <div style={{ marginTop: '0.4rem', opacity: 0.75 }}>
            {run.map((row, i) => (
              <div key={i} style={{ border: '1px solid var(--tm-border)', borderRadius: '6px', padding: '0.45rem 0.7rem', marginBottom: '0.35rem' }}>
                <div className="artifact-body" style={{ fontSize: '0.82rem' }}><Content content={(row.newBlock ?? row.oldBlock)?.text ?? ''} /></div>
              </div>
            ))}
          </div>
        )}
      </div>
    )
  }

  diff.rows.forEach((row, index) => {
    if (row.kind === 'same') {
      sameRun.push(row)
      return
    }
    flushSame()
    if (row.kind === 'modified') {
      rendered.push(
        <div key={index} style={{ margin: '0.55rem 0' }}>
          <div style={{ display: 'flex', gap: '0.55rem', flexWrap: 'wrap' }}>
            <BlockPanel format={format} label={`${t('markdown_diff.before') ?? 'Before'} · ${beforeLabel}`} text={row.oldBlock?.text ?? ''} tone="del" />
            <BlockPanel format={format} label={`${t('markdown_diff.after') ?? 'After'} · ${afterLabel}`} text={row.newBlock?.text ?? ''} tone="add" />
          </div>
          {row.changes && <>
            <div style={{ fontSize: '0.72rem', color: 'var(--tm-text-3)', marginTop: '0.35rem' }}>{t('markdown_diff.what_changed') ?? 'What changed'}:</div>
            <WordStrip changes={row.changes} />
          </>}
        </div>
      )
    } else if (row.kind === 'added') {
      rendered.push(
        <div key={index} style={{ margin: '0.55rem 0' }}>
          <BlockPanel format={format} label={`+ ${t('markdown_diff.added') ?? 'Added'}`} text={row.newBlock?.text ?? ''} tone="add" />
        </div>
      )
    } else {
      rendered.push(
        <div key={index} style={{ margin: '0.55rem 0' }}>
          <BlockPanel format={format} label={`− ${t('markdown_diff.removed') ?? 'Removed'}`} text={row.oldBlock?.text ?? ''} tone="del" />
        </div>
      )
    }
  })
  flushSame()

  return (
    <div>
      <div style={{ display: 'flex', gap: '0.45rem', flexWrap: 'wrap', alignItems: 'center', marginBottom: '0.65rem', fontSize: '0.75rem', color: 'var(--tm-text-2)' }}>
        <strong style={{ color: 'var(--tm-text)' }}>{title ?? (t('markdown_diff.title') ?? 'What changed')}</strong>
        <span style={{ color: 'var(--tm-success)' }}>+{diff.counts.added} {t('markdown_diff.added') ?? 'added'}</span>
        <span style={{ color: 'var(--tm-amber)' }}>~{diff.counts.modified} {t('markdown_diff.modified') ?? 'modified'}</span>
        <span style={{ color: 'var(--tm-danger)' }}>−{diff.counts.removed} {t('markdown_diff.removed') ?? 'removed'}</span>
        <span>{diff.counts.same} {t('markdown_diff.same') ?? 'unchanged'}</span>
      </div>
      {diff.counts.added + diff.counts.removed + diff.counts.modified === 0 && <div>{t('diff.no_changes')}</div>}
      {rendered}
    </div>
  )
}
