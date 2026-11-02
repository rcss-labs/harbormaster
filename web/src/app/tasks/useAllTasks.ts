import { useInfiniteQuery } from '@tanstack/react-query'
import { useEffect } from 'react'
import { api } from '../../api/client'
import { queryString, type Filters } from '../../api/hooks'
import type { TaskDTO, TasksListDTO } from '../../api/types'

/** The API caps a page at 500 rows. */
const PAGE_LIMIT = 500
/** At most this many pages are loaded (2,000 tasks), so a huge estate cannot make the page load without end. */
export const MAX_PAGES = 4

export interface AllTasks {
  items: TaskDTO[]
  /** Summary of the first page; it covers the whole scope, not just that page. */
  summary?: TasksListDTO['summary']
  /** True while pages are still being fetched. */
  loading: boolean
  /** True when the cap stopped loading before the API ran out of tasks. */
  truncated: boolean
  error: unknown
}

/**
 * Every task in scope, loaded page by page. Grouping and paging by 50 rows happen on the client, so the list must be
 * complete; the API pages by offset and cannot group.
 */
export function useAllTasks(filters: Filters): AllTasks {
  const query = useInfiniteQuery({
    queryKey: ['tasks', 'all', filters],
    queryFn: ({ pageParam }) =>
      api.get<TasksListDTO>(`/api/tasks${queryString({ ...filters, filter: 'all', limit: PAGE_LIMIT, cursor: pageParam })}`),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => last.next ?? undefined,
    refetchInterval: 5000,
    refetchIntervalInBackground: false,
  })
  const pages = query.data?.pages ?? []
  const { hasNextPage, isFetchingNextPage, fetchNextPage } = query
  const loadedPages = pages.length
  const canLoadMore = hasNextPage && pages.length < MAX_PAGES

  useEffect(() => {
    if (canLoadMore && !isFetchingNextPage) void fetchNextPage()
  }, [canLoadMore, isFetchingNextPage, fetchNextPage, loadedPages])

  return {
    items: pages.flatMap((page) => page.items),
    summary: pages[0]?.summary,
    loading: query.isPending || canLoadMore,
    truncated: hasNextPage && pages.length >= MAX_PAGES,
    error: query.error,
  }
}
