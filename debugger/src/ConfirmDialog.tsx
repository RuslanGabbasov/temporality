import { useCallback, useRef, useState, type ReactNode } from 'react'
import { Button } from '@carbon/react'
import AppModal from './Modal'
import { useT } from './i18n'

export type ConfirmRequest = {
  title: string
  body?: string
  confirmLabel?: string
  cancelLabel?: string
  /** danger renders the confirm button as destructive (delete/reject). */
  danger?: boolean
}

type Pending = { request: ConfirmRequest; resolve: (ok: boolean) => void }

/**
 * Promise-based replacement for the native confirm(): same call shape
 * (`if (await confirmDialog({...})) …`) but rendered through the shared
 * AppModal shell, so Escape/overlay-click cancel and the visual language
 * match the rest of the app. One hook instance per page; render the
 * returned `dialog` anywhere in the tree.
 */
export function useConfirm(): { confirmDialog: (request: ConfirmRequest) => Promise<boolean>; dialog: ReactNode } {
  const t = useT()
  const [pending, setPending] = useState<Pending | null>(null)
  // Resolve must survive unmount of the pending dialog owner: keep the latest
  // resolver on a ref and settle orphaned promises on close.
  const pendingRef = useRef<Pending | null>(null)
  const settle = useCallback((ok: boolean) => {
    pendingRef.current?.resolve(ok)
    pendingRef.current = null
    setPending(null)
  }, [])
  const confirmDialog = useCallback((request: ConfirmRequest) => new Promise<boolean>((resolve) => {
    pendingRef.current?.resolve(false)
    const next = { request, resolve }
    pendingRef.current = next
    setPending(next)
  }), [])
  const dialog = pending && (
    <AppModal onClose={() => settle(false)} panelStyle={{ width: '420px' }}>
      <h2 style={{ margin: 0, fontSize: '1rem', fontWeight: 600 }}>{pending.request.title}</h2>
      {pending.request.body && (
        <p style={{ margin: 0, fontSize: '0.85rem', color: 'var(--tm-text-2)', lineHeight: 1.5, whiteSpace: 'pre-wrap' }}>
          {pending.request.body}
        </p>
      )}
      <div className="form-actions" style={{ marginTop: 0 }}>
        <Button size="sm" kind="secondary" onClick={() => settle(false)}>{pending.request.cancelLabel ?? t('action.cancel') ?? 'Cancel'}</Button>
        <Button size="sm" kind={pending.request.danger ? 'danger' : 'primary'} onClick={() => settle(true)}>{pending.request.confirmLabel ?? t('action.confirm') ?? 'Confirm'}</Button>
      </div>
    </AppModal>
  )
  return { confirmDialog, dialog }
}
