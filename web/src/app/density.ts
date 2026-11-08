import { useSyncExternalStore } from 'react'

export const DENSITY_STORAGE_KEY = 'harbormaster-density'

export type Density = 'compact' | 'comfortable'

/** The density in effect: `comfortable` only when data-density says so. */
export function readDensity(): Density {
  return document.documentElement.dataset.density === 'comfortable' ? 'comfortable' : 'compact'
}

/** Applies the remembered density choice (called once at boot, before first paint). */
export function applyStoredDensity(): void {
  try {
    const stored = localStorage.getItem(DENSITY_STORAGE_KEY)
    if (stored === 'compact' || stored === 'comfortable') document.documentElement.dataset.density = stored
  } catch {
    // storage unavailable: keep the default density
  }
}

/** Switches between compact and comfortable and remembers the choice. */
export function toggleDensity(): Density {
  const next: Density = readDensity() === 'compact' ? 'comfortable' : 'compact'
  document.documentElement.dataset.density = next
  try {
    localStorage.setItem(DENSITY_STORAGE_KEY, next)
  } catch {
    // storage unavailable: the choice lasts for this page view only
  }
  return next
}

function subscribe(onChange: () => void): () => void {
  const observer = new MutationObserver(onChange)
  observer.observe(document.documentElement, { attributes: true, attributeFilter: ['data-density'] })
  return () => observer.disconnect()
}

/** The current density; re-renders when the toggle flips it. */
export function useDensity(): Density {
  return useSyncExternalStore(subscribe, readDensity)
}
