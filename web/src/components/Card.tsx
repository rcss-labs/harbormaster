import type { ReactNode } from 'react'
import styles from './Card.module.css'

interface CardProps {
  title: ReactNode
  right?: ReactNode
  children: ReactNode
}

export function Card({ title, right, children }: CardProps) {
  return (
    <div className={styles.card}>
      <div className={styles['card-head']}>
        <h2>{title}</h2>
        {right !== undefined && <span className={styles.right}>{right}</span>}
      </div>
      <div className={styles['card-body']}>{children}</div>
    </div>
  )
}
