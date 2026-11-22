import { useEffect, useRef, useState } from 'react'
import { api, apiFetch } from './client'
import { refetchFastFollow } from './change'

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
}

/** How often a visible page tells the server it is still open; the server's TTL is three times this. */
export const FOCUS_HEARTBEAT_MS = 10_000

const RECHECK_AFTER_OPEN_MS = 3000
/** How long after an open a missing live read is only pending: the poller's first read takes a few seconds to land. */
const PENDING_AFTER_OPEN_MS = 12_000

const UNAVAILABLE: FocusDTO = { state: 'unavailable', interval_seconds: 0, refreshed_at: null, message: 'Live refresh unavailable' }

/** Soon after an open, "unavailable" with no read yet only means the first read has not landed: show it as live, no pill. */
function pendingOrNot(dto: FocusDTO, openedAt: number): FocusDTO {
  if (dto.state === 'unavailable' && dto.refreshed_at === null && Date.now() - openedAt < PENDING_AFTER_OPEN_MS) {
    return { ...dto, state: 'live', message: '' }
  }
  return dto
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

    const send = (open: boolean) => {
      api
        .post<FocusDTO>('/api/focus', { kind, id, open })
        .then((dto) => active && setFocus(pendingOrNot(dto, openedAt)))
        .catch(() => active && setFocus(UNAVAILABLE))
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

  return focus
}
