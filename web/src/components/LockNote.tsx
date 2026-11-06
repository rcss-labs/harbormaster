import type { ReactNode } from 'react'
import styles from './LockNote.module.css'
import { usePhone } from './usePhone'

interface LockNoteProps {
  /** The lock reason; nothing renders when it is undefined. */
  children: ReactNode | undefined
  /** `why` is a quiet line under one locked control; the default is a note at the top of a locked panel or group. */
  kind?: 'note' | 'why'
}

/**
 * The visible form of a disabled control's reason on a phone, where a `title` tooltip is unreachable.
 * Desktop keeps the tooltip alone, so this renders nothing there.
 */
export function LockNote({ children, kind = 'note' }: LockNoteProps) {
  const phone = usePhone()
  if (!phone || children === undefined || children === null || children === false) return null
  return (
    <p className={kind === 'why' ? styles.why : styles.note} role="note">
      {children}
    </p>
  )
}
