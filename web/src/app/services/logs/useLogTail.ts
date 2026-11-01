import { useCallback, useEffect, useRef, useState } from 'react'
import type { LogLine } from '../../../api/types'

export type TailStatus = 'idle' | 'connecting' | 'live' | 'ended'

export const MAX_TAIL_LINES = 5000
export const KEEPALIVE_INTERVAL_MS = 60_000

interface Options {
  serviceId: string
  container?: string
  q?: string
  enabled: boolean
  csrf: string
}

interface TailState {
  key: string
  status: TailStatus
  lines: LogLine[]
  endReason?: string
  endMessage?: string
}

// Close codes whose reason is a human message to show, and the end reason each maps to.
const CLOSE_CODE_REASONS: Record<number, string> = {
  4400: 'bad_start',
  4401: 'csrf_invalid',
  4403: 'forbidden',
  4404: 'not_found',
  4408: 'idle',
  4409: 'tail_limit',
  4422: 'invalid',
  4502: 'error',
}
const HUMAN_REASON_CODES = new Set([4403, 4409, 4422])

function tailUrl(serviceId: string, container: string | undefined, q: string | undefined): string {
  const params = new URLSearchParams()
  if (container) params.set('container', container)
  if (q) params.set('q', q)
  const query = params.toString()
  const scheme = window.location.protocol === 'https:' ? 'wss' : 'ws'
  return `${scheme}://${window.location.host}/api/services/${encodeURIComponent(serviceId)}/logs/tail${query ? `?${query}` : ''}`
}

/** Live tail over WebSocket. `enabled` false closes the socket (the server then frees the tail slot); lines stay. */
export function useLogTail({ serviceId, container, q, enabled, csrf }: Options) {
  const [attempt, setAttempt] = useState(0)
  const key = `${serviceId}|${container ?? ''}|${q ?? ''}|${attempt}`
  const [state, setState] = useState<TailState>({ key, status: 'idle', lines: [] })
  const socketRef = useRef<WebSocket | null>(null)
  const lastKeepaliveRef = useRef(0)

  useEffect(() => {
    if (!enabled || !csrf) return
    const socket = new WebSocket(tailUrl(serviceId, container, q))
    socketRef.current = socket
    let finished = false
    const update = (patch: (current: TailState) => Partial<TailState>) =>
      setState((current) => {
        const base: TailState = current.key === key ? current : { key, status: 'connecting', lines: [] }
        return { ...base, ...patch(base) }
      })

    socket.onopen = () => socket.send(JSON.stringify({ type: 'start', csrf }))
    socket.onmessage = (event) => {
      let frame: { type?: string; items?: LogLine[]; reason?: string; message?: string }
      try {
        frame = JSON.parse(String(event.data))
      } catch {
        return
      }
      if (frame.type === 'ready') update(() => ({ status: 'live' }))
      else if (frame.type === 'lines' && frame.items) {
        const items = frame.items
        update((current) => ({ lines: [...current.lines, ...items].slice(-MAX_TAIL_LINES) }))
      } else if (frame.type === 'end') {
        finished = true
        update(() => ({ status: 'ended', endReason: frame.reason ?? 'closed', endMessage: frame.message }))
      }
    }
    socket.onclose = (event) => {
      // A close after an end frame keeps that frame's reason; otherwise derive the reason from the close code.
      if (finished) return
      finished = true
      const reason = CLOSE_CODE_REASONS[event.code] ?? 'closed'
      update(() => ({
        status: 'ended',
        endReason: reason,
        endMessage: HUMAN_REASON_CODES.has(event.code) ? event.reason : undefined,
      }))
    }
    return () => {
      finished = true
      socket.onopen = socket.onmessage = socket.onclose = null
      socket.close()
      if (socketRef.current === socket) socketRef.current = null
      // A later re-enable (Follow after Pause) opens a fresh connection; forget this one's status and end reason.
      setState((current) => (current.key === key ? { ...current, status: 'idle', endReason: undefined, endMessage: undefined } : current))
    }
  }, [serviceId, container, q, enabled, csrf, key])

  const resume = useCallback(() => setAttempt((n) => n + 1), [])

  /** Tells the server the user is still here; sent at most once a minute. */
  const keepalive = useCallback(() => {
    const socket = socketRef.current
    const now = Date.now()
    if (!socket || socket.readyState !== 1 || now - lastKeepaliveRef.current < KEEPALIVE_INTERVAL_MS) return
    lastKeepaliveRef.current = now
    socket.send(JSON.stringify({ type: 'keepalive' }))
  }, [])

  const current = state.key === key ? state : null
  const status: TailStatus = !enabled ? 'idle' : (current && current.status !== 'idle' ? current.status : 'connecting')
  return {
    status,
    lines: current?.lines ?? [],
    endReason: enabled ? current?.endReason : undefined,
    endMessage: enabled ? current?.endMessage : undefined,
    resume,
    keepalive,
  }
}
