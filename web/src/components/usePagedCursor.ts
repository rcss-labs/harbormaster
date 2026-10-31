import { useState } from 'react'

interface PagedState<T> {
  scope: string
  cursor?: string
  earlier: T[]
}

/**
 * Accumulates cursor-paged lists in state. `cursor` is what the current page request asks for;
 * `earlier` holds items from pages already loaded. A new `scope` (service, filter, account) starts again from page one.
 */
export function usePagedCursor<T>(scope: string) {
  const [state, setState] = useState<PagedState<T>>({ scope, earlier: [] })
  const current: PagedState<T> = state.scope === scope ? state : { scope, earlier: [] }
  return {
    cursor: current.cursor,
    earlier: current.earlier,
    loadMore: (pageItems: T[], nextCursor: string) =>
      setState({ scope, cursor: nextCursor, earlier: [...current.earlier, ...pageItems] }),
  }
}
