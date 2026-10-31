import { useState } from 'react'

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

export function Ago({ ts, now }: AgoProps) {
  const [mountedAt] = useState(() => Date.now())
  const time = typeof ts === 'number' ? ts : Date.parse(ts)
  return <span title={new Date(time).toLocaleString()}>{formatAgo(ts, now ?? mountedAt)}</span>
}
