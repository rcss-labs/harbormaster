import type { ClusterCardDTO } from '../../api/types'
import type { PillKind } from '../../components/Pill'
import { clusterStatusKind, clusterStatusLabel, statusKind, statusLabel } from '../../components/status'
import type { Pin } from './pins'

export interface PinStatus {
  kind: PillKind
  text: string
  /** Set when the loaded list does not contain the pin: destroyed, or not visible to this viewer. */
  missing?: boolean
}

/** Shown beside a missing pin: the page is unreachable for this viewer, whether it was destroyed or access was removed. */
export const missingPinHint = 'No longer exists or you no longer have access'

/**
 * The live status of a pin, read from the cluster list. The list is not scoped by the sidebar filters, but the API
 * still applies visibility, so a pin absent from a loaded list is missing for this viewer (see missingPinHint).
 * While the list is loading the status is "Status unknown".
 */
export function pinStatus(pin: Pin, clusters: ClusterCardDTO[] | undefined): PinStatus {
  if (!clusters) return { kind: 'mute', text: 'Status unknown' }
  if (pin.kind === 'cluster') {
    const cluster = clusters.find((c) => c.id === pin.id)
    return cluster ? { kind: clusterStatusKind(cluster.status), text: clusterStatusLabel(cluster.status) } : { kind: 'mute', text: 'Unavailable', missing: true }
  }
  const service = clusters.flatMap((c) => c.services).find((s) => s.id === pin.id)
  return service ? { kind: statusKind(service.status), text: statusLabel(service.status) } : { kind: 'mute', text: 'Unavailable', missing: true }
}
