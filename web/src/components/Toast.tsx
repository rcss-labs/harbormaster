import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import styles from './Toast.module.css'
import { usePhone } from './usePhone'

export const TOAST_DURATION_MS = 5200

type ToastKind = 'ok' | 'bad' | 'info' | 'warn'

interface ToastItem {
  id: number
  message: ReactNode
  kind: ToastKind
  sticky: boolean
}

export interface ToastApi {
  show: (message: ReactNode, kind?: ToastKind, opts?: { sticky?: boolean }) => void
}

const ToastContext = createContext<ToastApi | null>(null)

export function ToastProvider({ children }: { children: ReactNode }) {
  const [toasts, setToasts] = useState<ToastItem[]>([])
  const phone = usePhone()
  const nextId = useRef(1)
  const timers = useRef(new Set<ReturnType<typeof setTimeout>>())

  useEffect(() => {
    const pending = timers.current
    return () => pending.forEach(clearTimeout)
  }, [])

  const dismiss = useCallback((id: number) => setToasts((current) => current.filter((toast) => toast.id !== id)), [])

  const show = useCallback<ToastApi['show']>(
    (message, kind = 'info', opts) => {
      const id = nextId.current++
      const sticky = opts?.sticky ?? false
      setToasts((current) => [...current, { id, message, kind, sticky }])
      if (!sticky) {
        const timer = setTimeout(() => {
          timers.current.delete(timer)
          dismiss(id)
        }, TOAST_DURATION_MS)
        timers.current.add(timer)
      }
    },
    [dismiss],
  )

  const api = useMemo(() => ({ show }), [show])

  return (
    <ToastContext.Provider value={api}>
      {children}
      <div className={`${styles.toasts} ${phone ? styles.phone : ''}`.trim()} role="status" aria-live="polite">
        {toasts.map((toast) => (
          <div key={toast.id} className={`${styles.toast} ${toast.kind === 'info' ? '' : styles[toast.kind]}`.trim()}>
            <div>{toast.message}</div>
            {toast.sticky && (
              <button type="button" className={styles.dismiss} aria-label="Dismiss" onClick={() => dismiss(toast.id)}>
                ×
              </button>
            )}
          </div>
        ))}
      </div>
    </ToastContext.Provider>
  )
}

export function useToast(): ToastApi {
  const api = useContext(ToastContext)
  if (!api) throw new Error('useToast must be used inside <ToastProvider>')
  return api
}

/** Shows the API's nullable `warning` (the action worked but Harbormaster could not record it) as a warning toast. */
export function showWarning(toast: ToastApi, warning: string | null | undefined) {
  if (warning) toast.show(warning, 'warn')
}
