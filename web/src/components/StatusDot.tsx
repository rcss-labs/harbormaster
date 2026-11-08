import type { ReactNode } from 'react'
import type { ClusterStatus, Status } from '../api/types'
import type { PillKind } from './Pill'
import { clusterStatusKind, clusterStatusLabel, statusKind, statusLabel } from './status'
import styles from './StatusDot.module.css'

interface StatusDotProps {
  /** Service status from the API; `''` renders a dash. */
  status?: Status | ''
  clusterStatus?: ClusterStatus
  /** Explicit colour for ad-hoc states; `children` then supplies the text. */
  kind?: PillKind
  /** Shape only, no text. The text stays as the accessible name. */
  bare?: boolean
  children?: ReactNode
  title?: string
}

const SHAPE_CLASS: Record<PillKind, string> = {
  ok: 'st-ok',
  warn: 'st-warn',
  bad: 'st-bad',
  info: 'st-info',
  expected: 'st-exp',
  mute: 'st-mute',
}

/**
 * Status as shape + colour + text, so state never relies on colour alone: circle ok, triangle warn,
 * diamond bad, half-disc info, ring expected, dash mute. Replaces Pill in tables.
 */
export function StatusDot({ status, clusterStatus, kind, bare = false, children, title }: StatusDotProps) {
  let resolvedKind: PillKind = kind ?? 'mute'
  let text: ReactNode = children
  if (clusterStatus !== undefined) {
    resolvedKind = clusterStatusKind(clusterStatus)
    text ??= clusterStatusLabel(clusterStatus)
  } else if (status !== undefined) {
    resolvedKind = statusKind(status)
    text ??= statusLabel(status)
  }
  const shape = SHAPE_CLASS[resolvedKind]
  const className = `${styles.st} ${styles[shape]} ${bare ? styles.bare : ''}`.trim()
  if (bare) {
    return (
      <span className={className} title={title} role="img" aria-label={typeof text === 'string' ? text : undefined} />
    )
  }
  return (
    <span className={className} title={title}>
      {text}
    </span>
  )
}
