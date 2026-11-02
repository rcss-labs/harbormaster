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
  children?: ReactNode
  title?: string
}

/** 6px dot plus coloured text, no background. Replaces Pill in tables. */
export function StatusDot({ status, clusterStatus, kind, children, title }: StatusDotProps) {
  let resolvedKind: PillKind = kind ?? 'mute'
  let text: ReactNode = children
  if (clusterStatus !== undefined) {
    resolvedKind = clusterStatusKind(clusterStatus)
    text ??= clusterStatusLabel(clusterStatus)
  } else if (status !== undefined) {
    resolvedKind = statusKind(status)
    text ??= statusLabel(status)
  }
  return (
    <span className={`${styles.status} ${styles[resolvedKind]}`} title={title}>
      <span className={styles.dot} aria-hidden="true" />
      {text}
    </span>
  )
}
