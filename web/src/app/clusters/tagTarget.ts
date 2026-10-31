import type { TagDTO } from '../../api/types'

/** The tag chip that was clicked, passed to the (future) tag dialog. */
export interface TagTarget {
  kind: 'cluster' | 'service'
  id: string
  name: string
  tag: TagDTO
}

export type TagClickHandler = (target: TagTarget) => void
