import { useEffect, useRef, type ReactNode } from 'react'
import styles from './Popover.module.css'
import { useFocusReturn } from './useFocusReturn'

interface PopoverProps {
  open: boolean
  onClose: () => void
  /** Accessible name. */
  label: string
  align?: 'left' | 'right'
  /** Width in px; capped to the viewport. */
  width?: number
  children: ReactNode
  role?: 'dialog' | 'menu' | 'group'
}

/**
 * Anchored panel below its opener. Render it inside a `position: relative` wrapper next to the opener.
 * Esc and a click outside close it; focus moves in, and returns to the opener on close.
 */
export function Popover({ open, onClose, label, align = 'left', width = 340, children, role = 'dialog' }: PopoverProps) {
  if (!open) return null
  return (
    <PopoverBody onClose={onClose} label={label} align={align} width={width} role={role}>
      {children}
    </PopoverBody>
  )
}

function PopoverBody({ onClose, label, align, width, children, role }: Required<Omit<PopoverProps, 'open'>>) {
  const ref = useRef<HTMLDivElement>(null)
  const openerRef = useFocusReturn(ref)
  const onCloseRef = useRef(onClose)
  useEffect(() => {
    onCloseRef.current = onClose
  })

  useEffect(() => {
    const onPointerDown = (event: MouseEvent) => {
      const target = event.target as Node
      if (ref.current?.contains(target)) return
      // The opener toggles the popover itself; closing here too would reopen it on the same click.
      if (openerRef.current?.contains(target)) return
      onCloseRef.current()
    }
    document.addEventListener('mousedown', onPointerDown)
    return () => document.removeEventListener('mousedown', onPointerDown)
  }, [openerRef])

  return (
    <div
      ref={ref}
      role={role}
      aria-label={label}
      tabIndex={-1}
      className={`${styles.pop} ${align === 'right' ? styles.right : styles.left}`}
      style={{ width: `min(${width}px, calc(100vw - 24px))` }}
      onKeyDown={(event) => {
        if (event.key !== 'Escape') return
        // Keep an enclosing Drawer or Dialog open: Esc closes only the innermost layer.
        event.stopPropagation()
        onClose()
      }}
    >
      {children}
    </div>
  )
}
