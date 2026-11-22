import { useSyncExternalStore } from 'react'

// Which service and cluster pages are being polled fast by the server right now. A page's query reads this to drop its
// own 5 s interval: the fast-follow refetches and the heartbeat-driven refresh keep a focused page current instead.
// Kept apart from focus.ts so hooks.ts can read it without importing change.ts (which imports hooks.ts).

const liveCounts = new Map<string, number>()
const listeners = new Set<() => void>()

const keyOf = (kind: string, id: string) => `${kind}:${id}`

function notify() {
  listeners.forEach((listener) => listener())
}

/** Marks an object as live-focused until the returned function is called. Counted, so two mounted pages can overlap. */
export function markLiveFocused(kind: string, id: string): () => void {
  const key = keyOf(kind, id)
  liveCounts.set(key, (liveCounts.get(key) ?? 0) + 1)
  notify()
  return () => {
    const left = (liveCounts.get(key) ?? 1) - 1
    if (left > 0) liveCounts.set(key, left)
    else liveCounts.delete(key)
    notify()
  }
}

function subscribe(listener: () => void) {
  listeners.add(listener)
  return () => {
    listeners.delete(listener)
  }
}

/** True while some mounted page holds a live focus on the object, so its query must not also poll on a timer. */
export function useIsLiveFocused(kind: string, id: string): boolean {
  return useSyncExternalStore(
    subscribe,
    () => liveCounts.has(keyOf(kind, id)),
    () => false,
  )
}
