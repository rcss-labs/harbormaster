import { useState, type ButtonHTMLAttributes, type ReactNode } from 'react'
import { Link, type LinkProps } from 'react-router'
import { Popover } from './Popover'
import styles from './RowMenu.module.css'

interface RowMenuProps {
  /** Accessible name of both the trigger and the menu, e.g. "Actions for payments-api". */
  label: string
  /** `RowMenuButton` and `RowMenuLink` items; the menu closes after any of them is used. */
  children: ReactNode
}

/** One "Actions" button that opens a menu: the narrow-screen form of a row's action buttons. */
export function RowMenu({ label, children }: RowMenuProps) {
  const [open, setOpen] = useState(false)
  return (
    <span className={styles.wrap}>
      <button type="button" className={styles.trigger} aria-label={label} aria-haspopup="menu" aria-expanded={open} onClick={() => setOpen((current) => !current)}>
        Actions
      </button>
      <Popover open={open} onClose={() => setOpen(false)} label={label} role="menu" align="right" width={200}>
        <div className={styles.menu} onClick={() => setOpen(false)}>
          {children}
        </div>
      </Popover>
    </span>
  )
}

type ButtonProps = Omit<ButtonHTMLAttributes<HTMLButtonElement>, 'role' | 'type' | 'className'> & { danger?: boolean }

export function RowMenuButton({ danger = false, ...props }: ButtonProps) {
  return <button type="button" role="menuitem" className={`${styles.item} ${danger ? styles.danger : ''}`.trim()} {...props} />
}

export function RowMenuLink(props: Omit<LinkProps, 'role' | 'className'>) {
  return <Link role="menuitem" className={styles.item} {...props} />
}
