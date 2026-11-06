import { useSyncExternalStore } from 'react'

/** Viewport width below which row actions collapse into one menu. */
export const NARROW_QUERY = '(max-width: 700px)'

const subscribe = (notify: () => void) => {
  if (typeof window.matchMedia !== 'function') return () => {}
  const query = window.matchMedia(NARROW_QUERY)
  query.addEventListener('change', notify)
  return () => query.removeEventListener('change', notify)
}
const snapshot = () => typeof window.matchMedia === 'function' && window.matchMedia(NARROW_QUERY).matches

/** True on a narrow viewport. Always false where matchMedia is missing (jsdom). */
export function useNarrow(): boolean {
  return useSyncExternalStore(subscribe, snapshot, () => false)
}

/** The phone breakpoint: the same query as `NARROW_QUERY`, so "narrow" and "phone" mean one thing. */
export const PHONE_QUERY = NARROW_QUERY

/** True on a phone-width viewport. */
export const usePhone = useNarrow
