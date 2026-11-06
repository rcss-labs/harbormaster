import { useEffect, useId, useRef, type ReactNode } from 'react'
import { BottomSheet } from './BottomSheet'
import styles from './Dialog.module.css'
import { usePhone } from './useNarrow'

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

const KIND_ORDER = { default: 0, danger: 1, primary: 2 } as const

/** A centred modal on desktop; on phones a bottom sheet whose actions are full-width footer buttons, primary last. */
export function Dialog(props: DialogProps) {
  return usePhone() ? <PhoneDialog {...props} /> : <DesktopDialog {...props} />
}

function PhoneDialog({ title, sub, actions, onClose, children }: DialogProps) {
  const ordered = [...actions].sort((a, b) => KIND_ORDER[a.kind ?? 'default'] - KIND_ORDER[b.kind ?? 'default'])
  return (
    <BottomSheet
      open
      onClose={onClose}
      label={typeof title === 'string' ? title : ''}
      labelledByTitle
      title={title}
      sub={sub}
      initialFocus="input, select, textarea"
      footer={ordered.map((action) => (
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
    >
      {children}
    </BottomSheet>
  )
}

function DesktopDialog({ title, sub, actions, wide, onClose, children }: DialogProps) {
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
