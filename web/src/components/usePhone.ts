import { useSyncExternalStore } from 'react'

/** The phone breakpoint: row actions collapse, and the shell, sheets and tables switch to the phone layout. */
export const PHONE_QUERY = '(max-width: 700px)'

const subscribe = (notify: () => void) => {
  if (typeof window.matchMedia !== 'function') return () => {}
  const query = window.matchMedia(PHONE_QUERY)
  query.addEventListener('change', notify)
  return () => query.removeEventListener('change', notify)
}
const snapshot = () => typeof window.matchMedia === 'function' && window.matchMedia(PHONE_QUERY).matches

/** True on a phone-width viewport. Always false where matchMedia is missing (jsdom). */
export function usePhone(): boolean {
  return useSyncExternalStore(subscribe, snapshot, () => false)
}
