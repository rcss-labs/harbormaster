import type { ReactNode } from 'react'
import styles from './Banner.module.css'

export function Banner({ kind, children }: { kind: 'info' | 'warn' | 'bad'; children: ReactNode }) {
  return (
    <div className={`${styles.banner} ${styles[kind]}`}>
      <span>{children}</span>
    </div>
  )
}
