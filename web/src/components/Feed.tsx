import type { ReactNode } from 'react'
import { Ago } from './Ago'
import styles from './Feed.module.css'

export function Feed({ children }: { children: ReactNode }) {
  return <div className={styles.feed}>{children}</div>
}

interface FeedRowProps {
  ts: string | number
  source: 'ecs' | 'harbormaster'
  actor: string
  children: ReactNode
}

export function FeedRow({ ts, source, actor, children }: FeedRowProps) {
  const fromEcs = source === 'ecs'
  return (
    <div className={styles['feed-row']}>
      <span className={styles.muted}>
        <Ago ts={ts} />
      </span>
      <span className={`${styles.src} ${fromEcs ? styles.e : styles.q}`}>{fromEcs ? 'ECS event' : actor}</span>
      <span>{children}</span>
    </div>
  )
}
