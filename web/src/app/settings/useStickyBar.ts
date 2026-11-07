import { useEffect } from 'react'

/** Marks the page with `data-hm-bar` while a sticky action bar shows, so the content keeps clear of it. */
export function useStickyBar(active: boolean): void {
  useEffect(() => {
    if (!active) return
    document.documentElement.dataset.hmBar = ''
    return () => {
      delete document.documentElement.dataset.hmBar
    }
  }, [active])
}
