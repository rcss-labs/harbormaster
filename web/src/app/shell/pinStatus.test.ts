import { describe, expect, it } from 'vitest'
import { CLUSTERS, CLUSTER_CORE, SERVICE_PAYMENTS } from '../../test/fixtures'
import type { Pin } from './pins'
import { pinStatus } from './pinStatus'

const servicePin: Pin = { kind: 'service', id: SERVICE_PAYMENTS.id, name: 'payments-api', cluster: 'core-services' }
const clusterPin: Pin = { kind: 'cluster', id: CLUSTER_CORE.id, name: 'core-services', account_name: 'homelab-admin', account_id: '482109375512' }
const goneService: Pin = { kind: 'service', id: 'gone-service', name: 'edge-nginx', cluster: 'edge-proxy' }
const goneCluster: Pin = { kind: 'cluster', id: 'gone-cluster', name: 'old-cluster', account_name: 'homelab-admin', account_id: '1' }
const loaded = CLUSTERS.items

describe('pinStatus', () => {
  it('reports Status unknown while the cluster list is still loading', () => {
    expect(pinStatus(servicePin, undefined)).toEqual({ kind: 'mute', text: 'Status unknown' })
    expect(pinStatus(clusterPin, undefined)).toEqual({ kind: 'mute', text: 'Status unknown' })
  })

  it('reports a service missing from the loaded list as Unavailable and missing', () => {
    expect(pinStatus(goneService, loaded)).toEqual({ kind: 'mute', text: 'Unavailable', missing: true })
  })

  it('reports a cluster missing from the loaded list as Unavailable and missing', () => {
    expect(pinStatus(goneCluster, loaded)).toEqual({ kind: 'mute', text: 'Unavailable', missing: true })
  })

  it('reports the live status of a pinned service or cluster that is in the loaded list', () => {
    const service = pinStatus(servicePin, loaded)
    expect(service).toEqual({ kind: 'ok', text: 'Steady' })
    expect(service.missing).toBeUndefined()
    expect(pinStatus(clusterPin, loaded).missing).toBeUndefined()
  })
})
