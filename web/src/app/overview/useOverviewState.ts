import { useCallback, useState } from 'react'
import { useSearchParams } from 'react-router'

export type OverviewLayout = 'triage' | 'estate'
export type EstateGrouping = 'cluster' | 'account'
export type SeverityFilter = 'all' | 'failing' | 'warning' | 'progress'

const LAYOUT_KEY = 'harbormaster-overview-layout'
const SEVERITIES: SeverityFilter[] = ['all', 'failing', 'warning', 'progress']

function readLayout(): OverviewLayout {
  try {
    return localStorage.getItem(LAYOUT_KEY) === 'estate' ? 'estate' : 'triage'
  } catch {
    return 'triage'
  }
}

/** Triage or Estate; remembered per browser. */
export function useOverviewLayout(): [OverviewLayout, (layout: OverviewLayout) => void] {
  const [layout, setLayout] = useState<OverviewLayout>(readLayout)
  const change = useCallback((next: OverviewLayout) => {
    setLayout(next)
    try {
      localStorage.setItem(LAYOUT_KEY, next)
    } catch {
      // storage unavailable: the choice still holds for this visit
    }
  }, [])
  return [layout, change]
}

/** Writes one query parameter (or removes it for `null`) without touching the account/region/tag filters. */
function useParamSetter() {
  const [, setSearchParams] = useSearchParams()
  return useCallback(
    (updates: Record<string, string | null>) =>
      setSearchParams(
        (current) => {
          const next = new URLSearchParams(current)
          for (const [key, value] of Object.entries(updates)) {
            if (value === null) next.delete(key)
            else next.set(key, value)
          }
          return next
        },
        { replace: true },
      ),
    [setSearchParams],
  )
}

/** Severity chip of Needs attention (?sev=). */
export function useSeverityFilter(): [SeverityFilter, (value: SeverityFilter) => void] {
  const [params] = useSearchParams()
  const set = useParamSetter()
  const raw = params.get('sev')
  const value = SEVERITIES.find((severity) => severity === raw) ?? 'all'
  return [value, (next) => set({ sev: next === 'all' ? null : next })]
}

export interface EstateState {
  grouping: EstateGrouping
  problemsOnly: boolean
  query: string
  setGrouping: (grouping: EstateGrouping) => void
  setProblemsOnly: (on: boolean) => void
  setQuery: (query: string) => void
}

/** Estate controls (?group=, ?problems=0, ?q=); Problems only is on unless the URL says otherwise. */
export function useEstateState(): EstateState {
  const [params] = useSearchParams()
  const set = useParamSetter()
  return {
    grouping: params.get('group') === 'account' ? 'account' : 'cluster',
    problemsOnly: params.get('problems') !== '0',
    query: params.get('q') ?? '',
    setGrouping: (grouping) => set({ group: grouping === 'cluster' ? null : grouping }),
    setProblemsOnly: (on) => set({ problems: on ? null : '0' }),
    setQuery: (query) => set({ q: query === '' ? null : query }),
  }
}
