import { useParams, useSearchParams } from 'react-router'
import type { AnalyticsRange, AnalyticsTab } from '../../api/analytics'
import { useParamSetter } from '../overview/useOverviewState'
import { useFilters } from '../shell/useFilters'

export const ANALYTICS_TABS: AnalyticsTab[] = ['delivery', 'reliability', 'capacity', 'operations', 'cost']
export const ANALYTICS_RANGES: AnalyticsRange[] = ['24h', '7d', '30d', '90d']
export const DEFAULT_ANALYTICS_RANGE: AnalyticsRange = '30d'

export interface AnalyticsState {
  /** Null when the path segment is not a known tab; the page redirects then. */
  tab: AnalyticsTab | null
  range: AnalyticsRange
  setRange: (range: AnalyticsRange) => void
  /** Tag ids of the global filter (?tag=), shared with the shell. */
  tag: number[] | undefined
  setTags: (ids: number[]) => void
}

/** Tab from `/analytics/:tab`, range from `?range=` (30d when missing or unknown), tag from the global filter. */
export function useAnalyticsState(): AnalyticsState {
  const { tab: tabParam } = useParams()
  const [params] = useSearchParams()
  const setParam = useParamSetter()
  const { filters, setTags } = useFilters()
  const rangeParam = params.get('range')
  return {
    tab: ANALYTICS_TABS.find((tab) => tab === tabParam) ?? null,
    range: ANALYTICS_RANGES.find((range) => range === rangeParam) ?? DEFAULT_ANALYTICS_RANGE,
    setRange: (range) => setParam({ range }),
    tag: filters.tag,
    setTags,
  }
}
