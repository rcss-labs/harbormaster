// Types and hooks for the review read API. They mirror the contract of internal/api/reviews_dto.go.
import { useInfiniteQuery, useQuery, type UseQueryResult } from '@tanstack/react-query'
import { useState } from 'react'
import { useAuth } from '../app/auth/AuthProvider'
import { api } from './client'
import { LIVE_REFETCH_MS } from './polling'
import type { InvestigationEvidenceDTO } from './investigations'

export interface ReviewRunDTO {
  id: number
  review_id: number | null
  name: string
  type: string
  manual: boolean
  state: string
  reason: string
  period_label: string
  scope_label: string
  services_total: number
  scheduled_for: string
  requested_at: string
  started_at: string | null
  finished_at: string | null
  summary: string
  summary_replaced: boolean
  /** A test run: not sent to any route. Only an admin can read one. */
  test: boolean
}

export interface ReviewRunDetailDTO extends ReviewRunDTO {
  evidence: InvestigationEvidenceDTO[]
  evidence_hidden: number
}

export interface ReviewRunsPage {
  items: ReviewRunDTO[]
  next_cursor: string
}

export const reviewRunsKey = ['reviews', 'runs'] as const
export const reviewRunKey = (id: number) => ['reviews', 'run', id] as const

const LIST_REFETCH_MS = 15_000
const POLL_LIMIT_MS = 30 * 60_000

/** Only these two states are still moving; any other, an unknown one included, is final for polling. */
export const isMovingReview = (state: string | undefined) => state === 'requested' || state === 'running'

/** The next refetch delay: `ms` while something is moving and the view is under 30 minutes old, otherwise none. */
export const refetchDelay = (moving: boolean, startedAt: number, now: number, ms: number): number | false =>
  moving && now - startedAt < POLL_LIMIT_MS ? ms : false

/** The time this hook started, so polling gives up 30 minutes after the view opened. Not read during render. */
const useStartedAt = () => useState(() => Date.now())[0]

/** Every review request is off unless the current-user DTO says the agent is on. Stored results stay readable while no
 *  review runs, so this is not `reviews_enabled`, which only decides about the navigation entry. */
export function useReviewResultsEnabled(): boolean {
  return Boolean(useAuth().me?.agent_enabled)
}

/** Results the reader may read, newest first, a page at a time; refreshed every 15 s while one is waiting or running. */
export function useReviewRuns() {
  const enabled = useReviewResultsEnabled()
  const startedAt = useStartedAt()
  return useInfiniteQuery({
    queryKey: reviewRunsKey,
    queryFn: ({ pageParam }) => api.get<ReviewRunsPage>(`/api/reviews/runs${pageParam ? `?cursor=${encodeURIComponent(pageParam)}` : ''}`),
    initialPageParam: '',
    getNextPageParam: (last) => last.next_cursor || undefined,
    enabled,
    refetchInterval: (query) =>
      refetchDelay(Boolean(query.state.data?.pages.some((page) => page.items.some((item) => isMovingReview(item.state)))), startedAt, Date.now(), LIST_REFETCH_MS),
    refetchIntervalInBackground: false,
  })
}

/** One run with its evidence; a waiting or running one is polled every 5 s for at most 30 minutes. */
export function useReviewRun(id: number | null): UseQueryResult<ReviewRunDetailDTO> {
  const enabled = useReviewResultsEnabled()
  const startedAt = useStartedAt()
  return useQuery({
    queryKey: reviewRunKey(id ?? 0),
    queryFn: () => api.get<ReviewRunDetailDTO>(`/api/reviews/runs/${encodeURIComponent(String(id))}`),
    enabled: enabled && id !== null,
    refetchInterval: (query) => refetchDelay(isMovingReview(query.state.data?.state), startedAt, Date.now(), LIVE_REFETCH_MS),
    refetchIntervalInBackground: false,
  })
}
