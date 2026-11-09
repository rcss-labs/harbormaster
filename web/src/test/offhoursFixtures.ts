// Fixtures for the off-hours endpoints. The contract test checks them against the backend shape; the types for these
// bodies live in api/offhours.ts, so the fixtures stay typed against them here.
import { http, HttpResponse } from 'msw'
import type {
  OffHoursList,
  OffHoursPreview,
  OffHoursSchedule,
  OffHoursService,
  OffHoursTagOption,
  ServiceOffHours,
  WakeResponse,
} from '../api/offhours'

const NOW = '2026-11-07T21:00:00Z'
const TAG_STAGING = { id: 2, name: 'Staging', color: 'amber' as const, inherited: false }
const TAG_PROD = { id: 3, name: 'Prod', color: 'red' as const, inherited: false }
const TAG_DEV = { id: 1, name: 'Dev', color: 'neutral' as const, inherited: false }

/** Monday to Friday 07:00-20:00, 65 awake hours. */
export const WEEKDAY_GRID: boolean[][] = Array.from({ length: 7 }, (_, day) => Array.from({ length: 24 }, (_, hour) => day < 5 && hour >= 7 && hour < 20))

function service(fields: Partial<OffHoursService> & Pick<OffHoursService, 'service_id' | 'service' | 'cluster' | 'state'>): OffHoursService {
  return {
    account_name: 'staging-example',
    region: 'us-east-1',
    tag: TAG_STAGING,
    awake_tasks: 1,
    current_tasks: 0,
    until: null,
    reason: '',
    monthly_always_on: 9.01,
    looks_like_production: [],
    can_operate: true,
    ...fields,
  }
}

export const OFFHOURS_ASLEEP = service({
  service_id: 'staging-web-web',
  service: 'web',
  cluster: 'staging-web',
  state: 'asleep',
  until: '2026-11-09T06:00:00Z',
})
export const OFFHOURS_WOKEN = service({
  service_id: 'staging-web-api',
  service: 'api',
  cluster: 'staging-web',
  state: 'woken',
  awake_tasks: 2,
  current_tasks: 2,
  until: '2026-11-07T23:00:00Z',
  monthly_always_on: 35.9,
})
export const OFFHOURS_BLOCKED = service({
  service_id: 'staging-web-worker',
  service: 'worker',
  cluster: 'staging-web',
  state: 'blocked',
  reason:
    "Can't apply: Harbormaster's role in staging-example can't change auto scaling. The access module is probably deployed with read_only = true.",
})
export const OFFHOURS_UNPRICED = service({
  service_id: 'edge-proxy-cert-renewer',
  service: 'cert-renewer',
  cluster: 'edge-proxy',
  account_name: 'homelab-admin',
  tag: TAG_DEV,
  state: 'awake',
  current_tasks: 1,
  until: '2026-11-09T18:00:00Z',
  monthly_always_on: null,
})
export const OFFHOURS_PRODUCTION = service({
  service_id: 'core-services-payments-api',
  service: 'payments-api',
  cluster: 'core-services',
  account_name: 'homelab-admin',
  tag: TAG_PROD,
  state: 'pending',
  awake_tasks: 3,
  current_tasks: 3,
  monthly_always_on: 54.1,
  looks_like_production: ['AWS tag env=prod', 'Harbormaster tag "Prod"'],
  can_operate: false,
})
export const OFFHOURS_NOT_MANAGED = service({
  service_id: 'staging-web-batch',
  service: 'batch',
  cluster: 'staging-web',
  state: 'not_managed',
  awake_tasks: 0,
  reason: 'Not managed: already at 0 tasks.',
  monthly_always_on: null,
})

function schedule(fields: Partial<OffHoursSchedule> & Pick<OffHoursSchedule, 'id' | 'name' | 'tag_key' | 'tag_value'>): OffHoursSchedule {
  return {
    enabled: true,
    timezone: 'Europe/Berlin',
    awake: WEEKDAY_GRID,
    awake_hours_per_week: 65,
    monthly_saving: 24.7,
    unpriced_services: 0,
    service_count: 0,
    asleep_count: 0,
    blocked_count: 0,
    version: 1,
    updated_at: '2026-11-05T09:30:00Z',
    updated_by: 'sam',
    services: [],
    ...fields,
  }
}

export const OFFHOURS_STAGING = schedule({
  id: 1,
  name: 'Staging nights and weekends',
  tag_key: 'env',
  tag_value: 'staging',
  service_count: 4,
  asleep_count: 1,
  blocked_count: 1,
  unpriced_services: 0,
})
export const OFFHOURS_PREVIEW_ENVS = schedule({
  id: 2,
  name: 'Preview environments',
  tag_key: 'env',
  tag_value: 'preview',
  enabled: false,
  awake_hours_per_week: 45,
  monthly_saving: null,
  version: 3,
})

/** The detail of the staging schedule: asleep, woken, blocked and not-managed rows. */
export const OFFHOURS_STAGING_DETAIL: OffHoursSchedule = {
  ...OFFHOURS_STAGING,
  services: [OFFHOURS_ASLEEP, OFFHOURS_WOKEN, OFFHOURS_BLOCKED, OFFHOURS_NOT_MANAGED],
}

/** A schedule holding an unpriced EC2 service next to a priced one. */
export const OFFHOURS_UNPRICED_DETAIL: OffHoursSchedule = schedule({
  id: 3,
  name: 'Edge and staging',
  tag_key: 'team',
  tag_value: 'web',
  service_count: 2,
  unpriced_services: 1,
  services: [OFFHOURS_ASLEEP, OFFHOURS_UNPRICED],
})

export const OFFHOURS_LIST: OffHoursList = {
  items: [OFFHOURS_STAGING, OFFHOURS_PREVIEW_ENVS],
  monthly_saving_total: 24.7,
  prices_as_of: '2026-10-19',
  now: NOW,
}

export const OFFHOURS_LIST_EMPTY: OffHoursList = { items: [], monthly_saving_total: null, prices_as_of: '2026-10-19', now: NOW }

/** The editor preview for env=prod: it warns, and still lets the person save. */
export const OFFHOURS_PREVIEW_PRODUCTION: OffHoursPreview = {
  services: [OFFHOURS_PRODUCTION],
  overlaps: [{ schedule_id: 1, schedule_name: 'Staging nights and weekends', services: ['core-services/payments-api'] }],
  sleep_now: 0,
  monthly_saving: 33.5,
  awake_hours_per_week: 65,
}

export const OFFHOURS_PREVIEW_STAGING: OffHoursPreview = {
  services: [OFFHOURS_ASLEEP, OFFHOURS_WOKEN],
  overlaps: [],
  sleep_now: 1,
  monthly_saving: 24.7,
  awake_hours_per_week: 65,
}

export const OFFHOURS_TAGS: { items: OffHoursTagOption[] } = {
  items: [
    { key: 'env', value: 'dev', services: 3 },
    { key: 'env', value: 'prod', services: 4 },
    { key: 'env', value: 'staging', services: 4 },
  ],
}

export const SERVICE_OFFHOURS_ASLEEP: ServiceOffHours = {
  enrolled: true,
  schedule_name: 'Staging nights and weekends',
  timezone: 'Europe/Berlin',
  state: 'asleep',
  until: '2026-11-09T06:00:00Z',
  reason: '',
  can_operate: true,
}
export const SERVICE_OFFHOURS_WOKEN: ServiceOffHours = { ...SERVICE_OFFHOURS_ASLEEP, state: 'woken', until: '2026-11-07T23:00:00Z' }
export const SERVICE_OFFHOURS_BLOCKED: ServiceOffHours = {
  ...SERVICE_OFFHOURS_ASLEEP,
  state: 'blocked',
  until: null,
  reason: OFFHOURS_BLOCKED.reason,
}
export const SERVICE_OFFHOURS_NONE: ServiceOffHours = {
  enrolled: false,
  schedule_name: '',
  timezone: '',
  state: '',
  until: null,
  reason: '',
  can_operate: true,
}

export const WAKE_RESPONSE: WakeResponse = { woken_until: '2026-11-07T23:00:00Z' }

/** Default handlers for the off-hours routes; msw.ts merges them. Override per test with `server.use`. */
export const offhoursHandlers = [
  http.get('/api/offhours/schedules', () => HttpResponse.json(OFFHOURS_LIST)),
  http.get('/api/offhours/schedules/:id', ({ params }) => {
    const detail = [OFFHOURS_STAGING_DETAIL, OFFHOURS_UNPRICED_DETAIL, OFFHOURS_PREVIEW_ENVS].find((s) => String(s.id) === params.id)
    return detail ? HttpResponse.json(detail) : HttpResponse.json({ error: { code: 'not_found', message: 'Not found.' } }, { status: 404 })
  }),
  http.post('/api/offhours/schedules', () => HttpResponse.json(OFFHOURS_STAGING_DETAIL, { status: 201 })),
  http.put('/api/offhours/schedules/:id', () => HttpResponse.json(OFFHOURS_STAGING_DETAIL)),
  http.delete('/api/offhours/schedules/:id', () => new HttpResponse(null, { status: 204 })),
  http.post('/api/offhours/preview', () => HttpResponse.json(OFFHOURS_PREVIEW_STAGING)),
  http.get('/api/offhours/tags', () => HttpResponse.json(OFFHOURS_TAGS)),
  http.get('/api/services/:id/offhours', () => HttpResponse.json(SERVICE_OFFHOURS_NONE)),
  http.post('/api/services/:id/offhours/wake', () => HttpResponse.json(WAKE_RESPONSE)),
  http.post('/api/services/:id/offhours/sleep', () => HttpResponse.json({})),
]
