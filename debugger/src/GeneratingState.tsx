import { useT } from './i18n'

/**
 * In-modal generation state: replaces the form content while the builder
 * agent is working, so the process is unmistakably visible.
 */
export default function GeneratingState({ title, hint }: { title?: string; hint?: string }) {
  const t = useT()
  return (
    <div className="generating-state">
      <span className="spinner spinner-lg" />
      <div className="generating-title">{title ?? t('action.building') ?? 'Building…'}</div>
      {hint && <div className="generating-hint">{hint}</div>}
    </div>
  )
}
