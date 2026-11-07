import type { ServiceListDTO } from '../../api/types'
import { SERVICES_LIST } from '../../test/fixtures'

type ServiceListItem = ServiceListDTO['items'][number]

/**
 * SERVICES_LIST made consistent with the ALERTS fixture, where the open alerts are on image-resizer, catalog-api and
 * edge-nginx: the list's status and attention text match those alerts, and notifications is steady.
 * `overrides` then changes single services by name, so a test can make statuses and alerts differ.
 */
export function listMatchingAlerts(overrides: Record<string, Partial<ServiceListItem>> = {}): ServiceListDTO {
  const patches: Record<string, Partial<ServiceListItem>> = {
    notifications: { status: 'steady', attention: '', rollout: null },
    'edge-nginx': { status: 'deploying', attention: 'Rolling out revision 8.' },
    ...overrides,
  }
  return {
    ...SERVICES_LIST,
    items: SERVICES_LIST.items.map((item) => ({
      ...item,
      ...patches[item.name],
    })),
  }
}

/** The same estate with every service steady and no alert text. */
export const allSteadyList = (): ServiceListDTO => ({
  ...SERVICES_LIST,
  items: SERVICES_LIST.items.map((item) => ({
    ...item,
    status: 'steady',
    attention: '',
  })),
})
