import { Loading } from '@carbon/react'
import { useT } from './i18n'

/** Page-level loading marker: a small spinner centered on the screen.
 * Fixed positioning keeps it out of the layout flow (no content shift while
 * a tab refetches) and pointer-events:none keeps the page interactive —
 * unlike Carbon's full-screen overlay it never dims or blocks anything. */
export default function PageLoading() {
  const t = useT()
  return (
    <div className="page-loading" role="status" aria-label={t('common.loading') ?? 'Loading…'}>
      <Loading withOverlay={false} small />
    </div>
  )
}
