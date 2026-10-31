import type { ReactNode } from 'react'
import styles from './Tile.module.css'

interface TileProps {
  k: ReactNode
  v: ReactNode
  /** Muted unit or qualifier shown beside the value. */
  small?: ReactNode
  children?: ReactNode
}

export function Tile({ k, v, small, children }: TileProps) {
  return (
    <div className={styles.tile}>
      <div className={styles.k}>{k}</div>
      <div className={styles.v}>
        {v}
        {small !== undefined && <small>{small}</small>}
      </div>
      {children}
    </div>
  )
}
