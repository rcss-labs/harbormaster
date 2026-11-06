import { createContext, useContext, useEffect, useId, useRef, type ReactNode } from 'react'
import { createPortal } from 'react-dom'
import { Link } from 'react-router'
import styles from './BottomSheet.module.css'
import { Icon } from './Icon'
import type { IconName } from './icons'
import { trapTab, useFocusReturn } from './useFocusReturn'

export interface BottomSheetProps {
  open: boolean
  onClose: () => void
  /** Accessible name of the sheet. */
  label: string
  title?: ReactNode
  sub?: ReactNode
  /** Fill the visible height instead of fitting the content. */
  tall?: boolean
  footer?: ReactNode
  role?: 'dialog' | 'menu'
  /** Accessible name of the close button; defaults to "Close". */
  closeLabel?: string
  /** Name the sheet by its title instead of `label` (for titles that are not plain strings). */
  labelledByTitle?: boolean
  /** Element to focus on open instead of the first focusable one (the close button). */
  initialFocus?: string
  children: ReactNode
}

const SheetRoleContext = createContext<'dialog' | 'menu'>('dialog')

/** Ids of the open sheets, innermost last: only the innermost one answers Escape. */
const openSheets: string[] = []

/**
 * Phone replacement for popovers, drawers and dialogs: a portal sheet pinned to the bottom edge over a scrim.
 * It owns one history entry while open, so the system Back gesture closes it instead of leaving the page.
 */
export function BottomSheet({ open, ...rest }: BottomSheetProps) {
  if (!open) return null
  return createPortal(<SheetBody {...rest} />, document.body)
}

function SheetBody({ onClose, label, title, sub, tall = false, footer, role = 'dialog', closeLabel = 'Close', labelledByTitle = false, initialFocus, children }: Omit<BottomSheetProps, 'open'>) {
  const id = useId()
  const ref = useRef<HTMLDivElement>(null)
  useFocusReturn(ref, initialFocus)
  const onCloseRef = useRef(onClose)
  useEffect(() => {
    onCloseRef.current = onClose
  })

  // History: push one entry per sheet. A popstate that lands on an entry other than this sheet's closes it.
  const mountedRef = useRef(false)
  useEffect(() => {
    mountedRef.current = true
    if (history.state?.hmSheet !== id) history.pushState({ ...history.state, hmSheet: id }, '')
    const onPopState = () => {
      if (history.state?.hmSheet !== id) onCloseRef.current()
    }
    window.addEventListener('popstate', onPopState)
    return () => {
      window.removeEventListener('popstate', onPopState)
      mountedRef.current = false
      // Deferred so a StrictMode remount (which re-runs the effect at once) keeps its entry.
      queueMicrotask(() => {
        if (!mountedRef.current && history.state?.hmSheet === id) history.back()
      })
    }
  }, [id])

  useEffect(() => {
    openSheets.push(id)
    const onKeyDown = (event: KeyboardEvent) => {
      if (openSheets[openSheets.length - 1] !== id) return
      if (event.key === 'Escape') onCloseRef.current()
      else trapTab(event, ref.current)
    }
    document.addEventListener('keydown', onKeyDown)
    return () => {
      document.removeEventListener('keydown', onKeyDown)
      const at = openSheets.lastIndexOf(id)
      if (at >= 0) openSheets.splice(at, 1)
    }
  }, [id])

  const titleId = useId()
  return (
    <SheetRoleContext.Provider value={role}>
      <button type="button" className={styles.scrim} aria-hidden="true" tabIndex={-1} data-testid="sheet-scrim" onClick={() => onCloseRef.current()} />
      <div
        ref={ref}
        role={role}
        aria-modal="true"
        {...(labelledByTitle && title !== undefined ? { 'aria-labelledby': titleId } : { 'aria-label': label })}
        tabIndex={-1}
        className={`${styles.sheet} ${tall ? styles.tall : ''}`.trim()}
        data-hm-sheet=""
      >
        <div className={styles.grabber} aria-hidden="true" />
        <header className={styles.head}>
          <div className={styles.titles}>
            {title !== undefined && (
              <h2 id={titleId} className={styles.title}>
                {title}
              </h2>
            )}
            {sub !== undefined && <p className={styles.sub}>{sub}</p>}
          </div>
          <button type="button" className={styles.close} aria-label={closeLabel} onClick={() => onCloseRef.current()}>
            <Icon name="x" />
          </button>
        </header>
        <div className={styles.body}>{role === 'menu' ? <div className={styles.list}>{children}</div> : children}</div>
        {footer !== undefined && <footer className={styles.foot}>{footer}</footer>}
      </div>
    </SheetRoleContext.Provider>
  )
}

export interface SheetActionProps {
  icon?: IconName
  label: ReactNode
  /** Why the action is disabled or what it will do; shown inside the row, never as a tooltip alone. */
  why?: string
  danger?: boolean
  right?: ReactNode
  disabled?: boolean
  onClick?: () => void
  href?: string
  current?: boolean
}

/** One 52 px row of a sheet: a button, or a router link when `href` is set. */
export function SheetAction({ icon, label, why, danger = false, right, disabled = false, onClick, href, current = false }: SheetActionProps) {
  const sheetRole = useContext(SheetRoleContext)
  const role = sheetRole === 'menu' ? 'menuitem' : undefined
  const className = `${styles.row} ${danger ? styles.danger : ''}`.trim()
  const content = (
    <>
      {icon && <Icon name={icon} size={18} />}
      <span className={styles.text}>
        <span>{label}</span>
        {why !== undefined && <span className={styles.why}>{why}</span>}
      </span>
      {right !== undefined && <span className={styles.right}>{right}</span>}
    </>
  )
  if (href !== undefined && !disabled) {
    return (
      <Link to={href} role={role} className={className} aria-current={current ? 'page' : undefined} onClick={onClick}>
        {content}
      </Link>
    )
  }
  return (
    <button type="button" role={role} className={className} disabled={disabled} aria-current={current ? 'page' : undefined} onClick={onClick}>
      {content}
    </button>
  )
}
