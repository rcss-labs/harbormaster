import { useSyncExternalStore } from 'react'

export const THEME_STORAGE_KEY = 'harbormaster-theme'

type Theme = 'light' | 'dark'

/** Dark when data-theme="dark", or when no theme is set and the OS prefers dark. */
export function isDarkTheme(): boolean {
  const theme = document.documentElement.dataset.theme
  if (theme) return theme === 'dark'
  return typeof window.matchMedia === 'function' && window.matchMedia('(prefers-color-scheme: dark)').matches
}

/** Applies the remembered theme choice (called once at boot, before first paint). */
export function applyStoredTheme(): void {
  try {
    const stored = localStorage.getItem(THEME_STORAGE_KEY)
    if (stored === 'light' || stored === 'dark') document.documentElement.dataset.theme = stored
  } catch {
    // storage unavailable: keep following the OS preference
  }
}

/** Switches between light and dark, starting from whatever is effective now, and remembers the choice. */
export function toggleTheme(): Theme {
  const next: Theme = isDarkTheme() ? 'light' : 'dark'
  document.documentElement.dataset.theme = next
  try {
    localStorage.setItem(THEME_STORAGE_KEY, next)
  } catch {
    // storage unavailable: the choice lasts for this page view only
  }
  return next
}

function subscribe(onChange: () => void): () => void {
  const observer = new MutationObserver(onChange)
  observer.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] })
  return () => observer.disconnect()
}

/** True while the dark theme is in effect; re-renders when the toggle flips it. */
export function useIsDark(): boolean {
  return useSyncExternalStore(subscribe, isDarkTheme)
}
