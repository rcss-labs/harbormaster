import type { ReactNode } from 'react'
import styles from './Table.module.css'

/** Wraps `table.t`; put <thead>/<tbody> in children. Row/cell helpers: `tableClass`. */
export function Table({ children }: { children: ReactNode }) {
  return (
    <div className={styles['table-wrap']}>
      <table className={styles.t}>{children}</table>
    </div>
  )
}

/** Class names for cells and rows inside a Table: numeric column, name cell, clickable row, status stripe. */
export const tableClass = {
  num: styles.num,
  name: styles.name,
  link: styles.link,
  stripe: (kind: 'bad' | 'warn' | 'info' | 'expected') => `${styles.stripe} ${styles[kind]}`,
}
