import { createElement, useEffect, useRef } from 'react'
import type { ServiceDTO } from '../../api/types'
import { useToast } from '../../components/Toast'

interface Remembered {
  id: string
  rolloutState: string
}

/**
 * Watches the PRIMARY deployment across polls and toasts once when it leaves IN_PROGRESS.
 * After a circuit-breaker rollback the failed deployment turns ACTIVE (rollout FAILED) while a new
 * PRIMARY rolls back, so the remembered id is looked up across all deployments.
 */
export function useRolloutToast(service: ServiceDTO | undefined): void {
  const toast = useToast()
  const remembered = useRef<Remembered | null>(null)
  const announced = useRef(new Set<string>())

  useEffect(() => {
    if (!service) return
    const deployments = service.deployments
    const primary = deployments.find((d) => d.status === 'PRIMARY')
    const previous = remembered.current

    if (previous?.rolloutState === 'IN_PROGRESS' && !announced.current.has(previous.id)) {
      const tracked = deployments.find((d) => d.id === previous.id)
      if (tracked?.rollout_state === 'COMPLETED') {
        announced.current.add(tracked.id)
        toast.show(
          createElement(
            'span',
            null,
            createElement('strong', null, service.name),
            ` is running :${tracked.revision} on all ${service.desired} tasks.`,
          ),
          'ok',
        )
      } else if (tracked?.rollout_state === 'FAILED') {
        announced.current.add(tracked.id)
        const rolledBackTo =
          primary && primary.id !== tracked.id
            ? primary.revision
            : (deployments.find((d) => d.status === 'ACTIVE' && d.id !== tracked.id)?.revision ?? tracked.revision)
        toast.show(
          createElement(
            'span',
            null,
            createElement('strong', null, service.name),
            `: revision :${tracked.revision} failed to start. Rolled back to :${rolledBackTo}.`,
          ),
          'bad',
        )
      }
    }

    remembered.current = primary ? { id: primary.id, rolloutState: primary.rollout_state } : null
  }, [service, toast])
}
