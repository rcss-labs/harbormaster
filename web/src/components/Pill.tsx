import type { ReactNode } from 'react'
import styles from './Pill.module.css'

export type PillKind = 'ok' | 'info' | 'warn' | 'bad' | 'mute'

export function Pill({ kind, children }: { kind: PillKind; children: ReactNode }) {
  return <span className={`${styles.pill} ${styles[kind]}`}>{children}</span>
}
