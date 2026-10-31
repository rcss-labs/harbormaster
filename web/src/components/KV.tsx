import { Fragment, type ReactNode } from 'react'
import styles from './KV.module.css'

export function KV({ rows }: { rows: [label: ReactNode, value: ReactNode][] }) {
  return (
    <dl className={styles.kv}>
      {rows.map(([label, value], index) => (
        <Fragment key={index}>
          <dt>{label}</dt>
          <dd>{value}</dd>
        </Fragment>
      ))}
    </dl>
  )
}
