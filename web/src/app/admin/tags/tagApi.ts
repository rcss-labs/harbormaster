import type { QueryClient } from '@tanstack/react-query'
import { api } from '../../../api/client'
import type { TagColor, TagDTO, TagItemDTO } from '../../../api/types'

export const TAG_COLORS: TagColor[] = ['neutral', 'red', 'amber', 'blue', 'violet', 'green']
export const TAG_NAME_PATTERN = /^[A-Za-z][A-Za-z0-9 _-]{0,23}$/
export const TAG_NAME_MESSAGE = 'Use 1–24 letters, digits, spaces, hyphens or underscores, starting with a letter.'

export type AssignmentKind = 'cluster' | 'service'

/** Queries a tag change can affect: tag lists, assignments, cluster and service pages, and the signed-in user's tags. */
const AFFECTED_QUERY_KEYS = ['tags', 'tag-assignments', 'clusters', 'cluster', 'service', 'me'] as const

export function invalidateTagQueries(queryClient: QueryClient): void {
  for (const key of AFFECTED_QUERY_KEYS) void queryClient.invalidateQueries({ queryKey: [key] })
}

export function saveAssignment(kind: AssignmentKind, id: string, tagId: number | null): Promise<void> {
  const collection = kind === 'cluster' ? 'clusters' : 'services'
  return api.put<void>(`/api/tag-assignments/${collection}/${id}`, { tag_id: tagId })
}

/**
 * Maps a select or radio value to the assignment body. '' means inherit (services only).
 * Dev is the untagged state for a cluster, so choosing Dev on a cluster sends null.
 */
export function assignedTagId(kind: AssignmentKind, value: string, tags: TagItemDTO[]): number | null {
  if (value === '') return null
  const chosen = tags.find((tag) => tag.id === Number(value))
  return kind === 'cluster' && chosen?.builtin ? null : Number(value)
}

/** The tag a chip should show after choosing `value`. Inheriting shows the cluster's tag, marked inherited. */
export function chosenTag(value: string, tags: TagItemDTO[], clusterTag?: TagDTO): TagDTO | undefined {
  if (value === '') return clusterTag && { ...clusterTag, inherited: true }
  const chosen = tags.find((tag) => tag.id === Number(value))
  return chosen && { id: chosen.id, name: chosen.name, color: chosen.color, inherited: false }
}
