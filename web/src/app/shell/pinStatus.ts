import type { ClusterCardDTO } from '../../api/types'
import type { PillKind } from '../../components/Pill'
import { clusterStatusKind, clusterStatusLabel, statusKind, statusLabel } from '../../components/status'
import type { Pin } from './pins'

export interface PinStatus {
  kind: PillKind
  text: string
}

/** The live status of a pin, read from the unfiltered cluster list; "Status unknown" while the pin is not in it. */
export function pinStatus(pin: Pin, clusters: ClusterCardDTO[] | undefined): PinStatus {
  if (pin.kind === 'cluster') {
    const cluster = clusters?.find((c) => c.id === pin.id)
    return cluster ? { kind: clusterStatusKind(cluster.status), text: clusterStatusLabel(cluster.status) } : { kind: 'mute', text: 'Status unknown' }
  }
  const service = clusters?.flatMap((c) => c.services).find((s) => s.id === pin.id)
  return service ? { kind: statusKind(service.status), text: statusLabel(service.status) } : { kind: 'mute', text: 'Status unknown' }
}
