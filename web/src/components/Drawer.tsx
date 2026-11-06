import { useEffect, useId, useRef, type ReactNode } from 'react'
import { createPortal } from 'react-dom'
import styles from './Drawer.module.css'
import { Icon } from './Icon'
import { trapTab, useFocusReturn } from './useFocusReturn'

interface DrawerProps {
  open: boolean
  onClose: () => void
  title: ReactNode
  sub?: ReactNode
  /** 620px instead of 440px. */
  wide?: boolean
  footer?: ReactNode
  children: ReactNode
}

/** Right-hand modal panel over a scrim. Esc, the scrim and the close button close it; focus returns to the opener. */
export function Drawer({ open, onClose, title, sub, wide = false, footer, children }: DrawerProps) {
  if (!open) return null
  return createPortal(
    <DrawerBody onClose={onClose} title={title} sub={sub} wide={wide} footer={footer}>
      {children}
    </DrawerBody>,
    document.body,
  )
}

function DrawerBody({ onClose, title, sub, wide, footer, children }: Omit<DrawerProps, 'open'>) {
  const titleId = useId()
  const ref = useRef<HTMLDivElement>(null)
  useFocusReturn(ref)
  const onCloseRef = useRef(onClose)
  useEffect(() => {
    onCloseRef.current = onClose
  })

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') onCloseRef.current()
      else trapTab(event, ref.current)
    }
    document.addEventListener('keydown', onKeyDown)
    return () => document.removeEventListener('keydown', onKeyDown)
  }, [])

  return (
    <>
      <button type="button" className={styles.scrim} data-testid="drawer-scrim" aria-label="Close drawer" tabIndex={-1} onClick={onClose} />
      <div ref={ref} role="dialog" aria-modal="true" aria-labelledby={titleId} tabIndex={-1} className={`${styles.drawer} ${wide ? styles.wide : ''}`.trim()}>
        <header className={styles.head}>
          <div className={styles.titles}>
            <h2 id={titleId}>{title}</h2>
            {sub !== undefined && <p className={styles.sub}>{sub}</p>}
          </div>
          <button type="button" className={styles.close} aria-label="Close panel" onClick={onClose}>
            <Icon name="x" />
          </button>
        </header>
        <div className={styles.body}>{children}</div>
        {footer !== undefined && <footer className={styles.foot}>{footer}</footer>}
      </div>
    </>
  )
}
