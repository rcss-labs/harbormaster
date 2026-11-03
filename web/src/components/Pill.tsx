import type { ReactNode } from 'react'
import styles from './Pill.module.css'

export type PillKind = 'ok' | 'info' | 'warn' | 'bad' | 'mute' | 'expected'

export function Pill({ kind, children, title }: { kind: PillKind; children: ReactNode; title?: string }) {
  return (
    <span className={`${styles.pill} ${styles[kind]}`} title={title}>
      {children}
    </span>
  )
}
