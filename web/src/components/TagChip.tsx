import type { TagDTO } from '../api/types'
import styles from './TagChip.module.css'

interface TagChipProps {
  tag: TagDTO
  onClick?: () => void
  /** Name of the cluster the tag is inherited from; marks the chip as inherited. */
  inheritedFrom?: string
  /** What the tag is set on when not inherited. */
  kind?: 'cluster' | 'service'
}

export function TagChip({ tag, onClick, inheritedFrom, kind = 'service' }: TagChipProps) {
  const inherited = inheritedFrom !== undefined
  const title = inherited ? `Inherited from cluster ${inheritedFrom}` : `Tag set on this ${kind}`
  const className = `${styles.label} ${styles[`c-${tag.color}`] ?? ''} ${inherited ? styles.inh : ''}`.trim()
  if (onClick) {
    return (
      <button type="button" className={className} title={title} onClick={onClick}>
        {tag.name}
      </button>
    )
  }
  return (
    <span className={className} title={title}>
      {tag.name}
    </span>
  )
}
