import { useEffect, useId, useRef, type ReactNode } from 'react'
import styles from './Dialog.module.css'

export interface DialogAction {
  label: string
  kind?: 'primary' | 'danger' | 'default'
  onClick: () => void
  disabled?: boolean
}

interface DialogProps {
  title: ReactNode
  sub?: ReactNode
  actions: DialogAction[]
  wide?: boolean
  onClose: () => void
  children?: ReactNode
}

export function Dialog({ title, sub, actions, wide, onClose, children }: DialogProps) {
  const titleId = useId()
  const dialogRef = useRef<HTMLDivElement>(null)
  const onCloseRef = useRef(onClose)
  useEffect(() => {
    onCloseRef.current = onClose
  })

  // Focus the first form field on open, falling back to the first button.
  useEffect(() => {
    const root = dialogRef.current
    if (!root) return
    const target = root.querySelector<HTMLElement>('input, select, textarea') ?? root.querySelector<HTMLElement>('button')
    target?.focus()
  }, [])

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') onCloseRef.current()
    }
    document.addEventListener('keydown', onKeyDown)
    return () => document.removeEventListener('keydown', onKeyDown)
  }, [])

  return (
    <div
      className={styles['dlg-backdrop']}
      onMouseDown={(event) => {
        if (event.target === event.currentTarget) onClose()
      }}
    >
      <div
        ref={dialogRef}
        className={`${styles.dlg} ${wide ? styles.wide : ''}`.trim()}
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
      >
        <div className={styles['dlg-head']}>
          <h2 id={titleId}>{title}</h2>
          {sub !== undefined && <p>{sub}</p>}
        </div>
        <div className={styles['dlg-body']}>{children}</div>
        <div className={styles['dlg-foot']}>
          {actions.map((action) => (
            <button
              key={action.label}
              type="button"
              className={`${styles.btn} ${action.kind && action.kind !== 'default' ? styles[action.kind] : ''}`.trim()}
              disabled={action.disabled}
              onClick={action.onClick}
            >
              {action.label}
            </button>
          ))}
        </div>
      </div>
    </div>
  )
}
