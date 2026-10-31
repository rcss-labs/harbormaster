import { useEffect, useState } from 'react'

interface AgoProps {
  ts: string | number
  /** Reference time in epoch ms; defaults to Date.now(). */
  now?: number
}

/** Relative time, matching the mockup's ago(): s, min, h (under 48 h), then d. */
export function formatAgo(ts: string | number, now: number): string {
  const time = typeof ts === 'number' ? ts : Date.parse(ts)
  const seconds = Math.max(0, Math.round((now - time) / 1000))
  if (seconds < 60) return `${seconds}s ago`
  const minutes = Math.round(seconds / 60)
  if (minutes < 60) return `${minutes} min ago`
  const hours = Math.round(minutes / 60)
  if (hours < 48) return `${hours} h ago`
  return `${Math.round(hours / 24)} d ago`
}

/** Current time in epoch ms, refreshed every `intervalMs`. */
export function useNow(intervalMs: number): number {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), intervalMs)
    return () => clearInterval(timer)
  }, [intervalMs])
  return now
}

const AGO_REFRESH_MS = 15_000

/** The API sends the zero time (0001-01-01T00:00:00Z) before the first poll; anything this old means "no data yet". */
const NO_DATA_BEFORE_MS = Date.UTC(1970, 0, 1)

export function Ago({ ts, now }: AgoProps) {
  const liveNow = useNow(AGO_REFRESH_MS)
  const time = typeof ts === 'number' ? ts : Date.parse(ts)
  if (time < NO_DATA_BEFORE_MS) return <span>no data yet</span>
  return <span title={new Date(time).toLocaleString()}>{formatAgo(ts, now ?? liveNow)}</span>
}
