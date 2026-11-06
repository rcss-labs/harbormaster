import { useSyncExternalStore } from 'react'
import { PHONE_QUERY } from '../components/useNarrow'

export const SCALE_STORAGE_KEY = 'harbormaster-scale'

export type Scale = 'auto' | '100' | '125' | '150' | '200'
export type EffectiveScale = 100 | 125 | 150 | 200

/** Windows at least this wide (a 4K display at 100 % OS scaling) get 150 % when the choice is Auto. */
export const AUTO_SCALE_MIN_WIDTH = 3000
const RESIZE_DEBOUNCE_MS = 150

/** The choice kept in memory when localStorage is unavailable. */
let memoryScale: Scale | null = null

const SCALES: readonly Scale[] = ['auto', '100', '125', '150', '200']

/** The remembered choice, `auto` when nothing valid is stored. */
export function readScale(): Scale {
  try {
    const stored = localStorage.getItem(SCALE_STORAGE_KEY)
    return SCALES.includes(stored as Scale) ? (stored as Scale) : 'auto'
  } catch {
    return memoryScale ?? 'auto'
  }
}

/** The zoom percentage in effect. Phones never scale; Auto is 150 on wide windows and 100 otherwise. */
export function resolveScale(scale: Scale, innerWidth: number, phone: boolean): EffectiveScale {
  if (phone) return 100
  if (scale === 'auto') return innerWidth >= AUTO_SCALE_MIN_WIDTH ? 150 : 100
  return Number(scale) as EffectiveScale
}

const isPhone = () => typeof window.matchMedia === 'function' && window.matchMedia(PHONE_QUERY).matches

export interface ScaleState {
  scale: Scale
  effective: EffectiveScale
}

let snapshot: ScaleState | null = null
const listeners = new Set<() => void>()

/** Recomputes the effective scale, mirrors it to `data-scale` (absent at 100) and tells subscribers if it changed. */
function refresh(): ScaleState {
  const scale = readScale()
  const effective = resolveScale(scale, window.innerWidth, isPhone())
  if (effective === 100) delete document.documentElement.dataset.scale
  else document.documentElement.dataset.scale = String(effective)
  if (!snapshot || snapshot.scale !== scale || snapshot.effective !== effective) {
    snapshot = { scale, effective }
    listeners.forEach((notify) => notify())
  }
  return snapshot
}

/**
 * Applies the remembered choice (called once at boot, before first paint) and keeps Auto current:
 * the effective value is recomputed on resize (debounced) and when the phone query flips.
 * Returns a function that stops listening.
 */
export function applyStoredScale(): () => void {
  refresh()
  let timer: ReturnType<typeof setTimeout> | undefined
  const onResize = () => {
    clearTimeout(timer)
    timer = setTimeout(refresh, RESIZE_DEBOUNCE_MS)
  }
  window.addEventListener('resize', onResize)
  const phoneQuery = typeof window.matchMedia === 'function' ? window.matchMedia(PHONE_QUERY) : null
  phoneQuery?.addEventListener('change', refresh)
  return () => {
    clearTimeout(timer)
    window.removeEventListener('resize', onResize)
    phoneQuery?.removeEventListener('change', refresh)
  }
}

/** Remembers a new choice and applies it right away. */
export function setScale(scale: Scale): void {
  try {
    localStorage.setItem(SCALE_STORAGE_KEY, scale)
  } catch {
    // storage unavailable: the choice lasts for this page view only
    memoryScale = scale
  }
  refresh()
}

function subscribe(notify: () => void): () => void {
  listeners.add(notify)
  return () => listeners.delete(notify)
}

/** The chosen scale and the one in effect; re-renders when either changes. */
export function useScale(): ScaleState {
  return useSyncExternalStore(subscribe, () => snapshot ?? refresh())
}
