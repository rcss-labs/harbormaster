import { useEffect, useRef, useState } from 'react'
import { api, apiFetch } from './client'
import { refetchFastFollow } from './change'
import { markLiveFocused } from './focusRegistry'

export type FocusKind = 'cluster' | 'service'
export type FocusState = 'live' | 'capped' | 'user_capped' | 'throttled' | 'unavailable'

export interface FocusDTO {
  state: FocusState
  /** The effective focus interval, or the services tier interval when the page is not live. */
  interval_seconds: number
  /** The last successful focus read; null until one lands. */
  refreshed_at: string | null
  /** Empty, or a sentence for the pill's title. */
  message: string
  /** True for a definite fault (store down, registry error); false for a page only waiting for its first read. */
  failed: boolean
}

/** How often a visible page tells the server it is still open; the server's TTL is three times this. */
export const FOCUS_HEARTBEAT_MS = 10_000

const RECHECK_AFTER_OPEN_MS = 3000
/** How long after an open a missing live read is only pending: the poller's first read takes a few seconds to land. */
const PENDING_AFTER_OPEN_MS = 12_000

const UNAVAILABLE: FocusDTO = { state: 'unavailable', interval_seconds: 0, refreshed_at: null, message: 'Live refresh unavailable', failed: true }

/**
 * Soon after an open, "unavailable" with no read yet only means the first read has not landed: show it as live, no pill.
 * A definite failure the server flagged (store down, registry error) is never pending.
 */
function pendingOrNot(dto: FocusDTO, openedAt: number): FocusDTO {
  if (dto.state === 'unavailable' && !dto.failed && dto.refreshed_at === null && Date.now() - openedAt < PENDING_AFTER_OPEN_MS) {
    return { ...dto, state: 'live', message: '' }
  }
  return dto
}

function sameFocus(a: FocusDTO, b: FocusDTO): boolean {
  return a.state === b.state && a.interval_seconds === b.interval_seconds && a.refreshed_at === b.refreshed_at && a.message === b.message && a.failed === b.failed
}

const isVisible = () => document.visibilityState === 'visible'

/**
 * Keeps the open page polled every few seconds by the server while its tab is visible. It sends a focus heartbeat on
 * mount and every FOCUS_HEARTBEAT_MS, asks for an immediate read when the page opens or the tab comes back, refetches
 * the page's query shortly after each open, and releases the object (a keep-alive DELETE) when the tab is hidden or the
 * page goes away. A failed heartbeat reads as unavailable and heartbeating goes on. No request is made while hidden.
 */
export function useFocus(kind: FocusKind, id: string, refetch: () => Promise<unknown>): FocusDTO | undefined {
  const [focus, setFocus] = useState<FocusDTO | undefined>()
  const refetchRef = useRef(refetch)
  useEffect(() => {
    refetchRef.current = refetch
  })

  useEffect(() => {
    let active = true
    let timer: ReturnType<typeof setInterval> | undefined
    let cancelFastFollow: (() => void) | undefined
    let openedAt = 0
    let lastRefreshedAt: string | null | undefined

    const send = (open: boolean) => {
      api
        .post<FocusDTO>('/api/focus', { kind, id, open })
        .then((dto) => {
          if (!active) return
          // The server read the object again since the last answer: pull the new data now. The 5 s query timer is off
          // while the page is live, and the fast-follow refetches cover only the first seconds after an open.
          if (lastRefreshedAt !== undefined && dto.refreshed_at !== null && dto.refreshed_at !== lastRefreshedAt) void refetchRef.current()
          lastRefreshedAt = dto.refreshed_at
          setFocus((previous) => {
            const next = pendingOrNot(dto, openedAt)
            return previous && sameFocus(previous, next) ? previous : next
          })
        })
        .catch(() => {
          if (!active) return
          setFocus((previous) => (previous && sameFocus(previous, UNAVAILABLE) ? previous : UNAVAILABLE))
        })
    }
    const release = () => {
      apiFetch('/api/focus', { method: 'DELETE', body: JSON.stringify({ kind, id, open: false }), keepalive: true }).catch(() => undefined)
    }
    const start = () => {
      openedAt = Date.now()
      send(true)
      const cancelRefetches = refetchFastFollow(() => refetchRef.current())
      // The first live read lands a moment after the open; ask again then, so the pill does not wait for the next heartbeat.
      const recheck = setTimeout(() => send(false), RECHECK_AFTER_OPEN_MS)
      cancelFastFollow = () => {
        cancelRefetches()
        clearTimeout(recheck)
      }
      timer = setInterval(() => send(false), FOCUS_HEARTBEAT_MS)
    }
    const stop = () => {
      clearInterval(timer)
      timer = undefined
      cancelFastFollow?.()
      release()
    }
    const onVisibility = () => {
      if (isVisible()) {
        if (timer === undefined) start()
      } else if (timer !== undefined) {
        stop()
      }
    }

    if (isVisible()) start()
    document.addEventListener('visibilitychange', onVisibility)
    return () => {
      active = false
      document.removeEventListener('visibilitychange', onVisibility)
      if (timer !== undefined) stop()
    }
  }, [kind, id])

  // Only a confirmed live read counts: while the first read is still pending the page keeps its own timer.
  const live = focus?.state === 'live' && focus.refreshed_at !== null
  useEffect(() => (live ? markLiveFocused(kind, id) : undefined), [kind, id, live])

  return focus
}
