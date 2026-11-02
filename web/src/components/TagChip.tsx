import type { TagDTO } from '../api/types'
import styles from './TagChip.module.css'

interface TagChipProps {
  tag: TagDTO
  onClick?: () => void
  /** Name of the cluster the tag is inherited from; used in the title. */
  inheritedFrom?: string
  /** What the tag is set on when not inherited. */
  kind?: 'cluster' | 'service'
  /** Force the border style; by default inherited tags are dashed and the rest solid. */
  variant?: 'solid' | 'dashed'
}

export function TagChip({ tag, onClick, inheritedFrom, kind = 'service', variant }: TagChipProps) {
  const inherited = tag.inherited || inheritedFrom !== undefined
  const title = inherited
    ? `Inherited from cluster${inheritedFrom !== undefined ? ` ${inheritedFrom}` : ''}` : `Tag set on this ${kind}`
  const className = `${styles.label} ${styles[`c-${tag.color}`] ?? ''} ${(variant ? variant === 'dashed' : inherited) ? styles.inh : ''}`.trim()
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
