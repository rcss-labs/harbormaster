import { useCallback, useState } from 'react'

export interface AnalyticsPrefs {
  /** Source footers on panels and the "Data used on this page" table. */
  showSources: boolean
}

const KEY = 'harbormaster-analytics-prefs'
const DEFAULTS: AnalyticsPrefs = { showSources: true }

function read(): AnalyticsPrefs {
  try {
    const parsed: unknown = JSON.parse(localStorage.getItem(KEY) ?? '{}')
    if (parsed && typeof parsed === 'object') {
      const { showSources } = parsed as Record<string, unknown>
      return {
        showSources: typeof showSources === 'boolean' ? showSources : DEFAULTS.showSources,
      }
    }
  } catch {
    // corrupt or unavailable storage: use the defaults
  }
  return DEFAULTS
}

/** Display preferences of the Analytics page; remembered per browser. */
export function useAnalyticsPrefs(): [AnalyticsPrefs, (patch: Partial<AnalyticsPrefs>) => void] {
  const [prefs, setPrefs] = useState<AnalyticsPrefs>(read)
  const update = useCallback((patch: Partial<AnalyticsPrefs>) => {
    setPrefs((current) => {
      const next = { ...current, ...patch }
      try {
        localStorage.setItem(KEY, JSON.stringify(next))
      } catch {
        // storage unavailable: the choice still holds for this visit
      }
      return next
    })
  }, [])
  return [prefs, update]
}
