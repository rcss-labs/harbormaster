import type { ClusterStatus, Status } from '../api/types'
import type { PillKind } from './Pill'

const STATUS_KIND: Record<Status, PillKind> = {
  steady: 'ok',
  deploying: 'info',
  degraded: 'warn',
  unhealthy_task: 'warn',
  failing: 'bad',
  scaled_to_zero: 'mute',
}

const STATUS_LABEL: Record<Status, string> = {
  steady: 'Steady',
  deploying: 'Deploying',
  degraded: 'Degraded',
  unhealthy_task: 'Unhealthy task',
  failing: 'Failing',
  scaled_to_zero: 'Scaled to 0',
}

const CLUSTER_STATUS_KIND: Record<ClusterStatus, PillKind> = {
  healthy: 'ok',
  degraded: 'warn',
  deploying: 'info',
  needs_attention: 'bad',
}

const CLUSTER_STATUS_LABEL: Record<ClusterStatus, string> = {
  healthy: 'Healthy',
  degraded: 'Degraded',
  deploying: 'Deploying',
  needs_attention: 'Needs attention',
}

/** `''` is what the API sends for an event with no service, or whose service is gone. */
export const statusKind = (status: Status | ''): PillKind => (status === '' ? 'mute' : STATUS_KIND[status])
export const statusLabel = (status: Status | ''): string => (status === '' ? '–' : STATUS_LABEL[status])
export const clusterStatusKind = (status: ClusterStatus): PillKind => CLUSTER_STATUS_KIND[status]
export const clusterStatusLabel = (status: ClusterStatus): string => CLUSTER_STATUS_LABEL[status]

/** Chart/dot colour per pill kind (mockup STATUS_COLOR). */
export const STATUS_COLOR: Record<PillKind, string> = {
  ok: 'var(--success)',
  warn: 'var(--warning)',
  bad: 'var(--danger)',
  info: 'var(--info)',
  mute: 'var(--border-strong)',
  expected: 'var(--expected)',
}

export const SERIES = [
  'var(--series-1)',
  'var(--series-2)',
  'var(--series-3)',
  'var(--series-4)',
  'var(--series-5)',
  'var(--series-6)',
]

export const REASON_COLOR: Record<string, string> = {
  'Out of memory (exit 137)': 'var(--danger)',
  'Failed load balancer health checks': 'var(--warning)',
  'Stopped by a user': 'var(--series-3)',
  'Scaled in': 'var(--series-4)',
  'Replaced by a deployment': 'var(--border-strong)',
  'Failed to start': 'var(--danger)',
  'Container exited with an error': 'var(--danger)',
}
