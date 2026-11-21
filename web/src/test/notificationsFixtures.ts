// Fixtures for the alert notification endpoints. The contract test checks them against the backend shape; the
// types live in api/notifications.ts.
import type {
  NotificationDeliveries,
  NotificationDelivery,
  NotificationSummary,
  NotificationPreview,
  NotificationRoute,
  NotificationRoutes,
  NotificationSelector,
  NotificationTargets,
} from '../api/notifications'

const UPDATED = '2026-11-21T10:00:00Z'

export const NOTIFICATION_KINDS = [
  { kind: 'service_failing', label: 'Service failing' },
  { kind: 'service_degraded', label: 'Service degraded' },
  { kind: 'restart_loop', label: 'Restart loop' },
  { kind: 'unhealthy_task', label: 'Unhealthy tasks or targets' },
  { kind: 'deployment_failed', label: 'Deployment failed' },
  { kind: 'cannot_place', label: 'Cannot place tasks' },
  { kind: 'account_unreachable', label: 'Account unreachable' },
]
const ALL_KINDS = NOTIFICATION_KINDS.map((k) => k.kind)

/** A selector with every field present, as the API sends it. */
export function selector(fields: Partial<NotificationSelector> & Pick<NotificationSelector, 'kind'>): NotificationSelector {
  return {
    account_id: '',
    account_name: '',
    region: '',
    cluster_id: '',
    cluster: '',
    service_id: '',
    service: '',
    key: '',
    value: '',
    tag_id: 0,
    tag_name: '',
    missing: false,
    ...fields,
  }
}

function route(fields: Partial<NotificationRoute> & Pick<NotificationRoute, 'id' | 'name' | 'destination' | 'target'>): NotificationRoute {
  return {
    enabled: true,
    label: '',
    recipients: [],
    pagerduty_region: '',
    chat_id: '',
    thread_id: null,
    secret_set: true,
    signed: false,
    secret_unreadable: false,
    min_severity: 'warning',
    kinds: ALL_KINDS,
    repeat_seconds: 0,
    quiet_enabled: false,
    quiet_timezone: 'Europe/Berlin',
    notify_resolved: true,
    scope: [],
    fallback: false,
    matches_nothing: false,
    failing: false,
    version: 1,
    updated_at: UPDATED,
    ...fields,
  }
}

export const CORE_SERVICES_CLUSTER = selector({
  kind: 'cluster',
  account_id: '482109375512',
  account_name: 'homelab-admin',
  region: 'us-east-1',
  cluster_id: 'NDgyMTA5Mzc1NTEyfHVzLWVhc3QtMXxjb3JlLXNlcnZpY2Vz',
  cluster: 'core-services',
})

export const IMAGE_RESIZER_SERVICE = selector({
  kind: 'service',
  account_id: '482109375512',
  account_name: 'homelab-admin',
  region: 'us-east-1',
  cluster_id: 'NDgyMTA5Mzc1NTEyfHVzLWVhc3QtMXxiYWNrZ3JvdW5kLWpvYnM',
  cluster: 'background-jobs',
  service_id: 'NDgyMTA5Mzc1NTEyfHVzLWVhc3QtMXxiYWNrZ3JvdW5kLWpvYnN8aW1hZ2UtcmVzaXplcg',
  service: 'image-resizer',
})

export const ROUTE_PROD_PAGERDUTY = route({
  id: 1,
  name: 'Prod incidents to on-call',
  destination: 'pagerduty',
  target: 'On-call',
  label: 'On-call',
  pagerduty_region: 'us',
  min_severity: 'critical',
  repeat_seconds: 1800,
})
export const ROUTE_CORE_SLACK = route({
  id: 2,
  name: 'core-services to #core-alerts',
  destination: 'slack',
  target: 'hooks.slack.com',
  quiet_enabled: true,
  scope: [CORE_SERVICES_CLUSTER],
  version: 3,
})
export const ROUTE_RESIZER_TELEGRAM = route({
  id: 3,
  name: 'image-resizer to Telegram',
  destination: 'telegram',
  target: '-1001234567890',
  chat_id: '-1001234567890',
  thread_id: 7,
  scope: [IMAGE_RESIZER_SERVICE],
})
export const ROUTE_STAGING_EMAIL = route({
  id: 4,
  name: 'Staging account by email',
  destination: 'email',
  target: 'platform@example.com',
  recipients: ['platform@example.com'],
  secret_set: false,
  enabled: false,
  scope: [selector({ kind: 'account', account_id: '731022849906', account_name: 'staging-example' })],
})
export const ROUTE_CATCH_ALL = route({
  id: 5,
  name: 'Everything else',
  destination: 'slack',
  target: 'hooks.slack.com',
  fallback: true,
  failing: true,
})
export const ROUTE_WEBHOOK_SIGNED = route({
  id: 6,
  name: 'Audit webhook',
  destination: 'webhook',
  target: 'audit.example.com',
  signed: true,
  scope: [
    selector({ kind: 'aws', key: 'env', value: 'staging' }),
    selector({ kind: 'harbormaster', tag_id: 2, tag_name: 'Staging' }),
    selector({
      kind: 'service',
      account_id: '482109375512',
      account_name: 'homelab-admin',
      region: 'us-east-1',
      cluster: 'core-services',
      cluster_id: 'NDgyMTA5Mzc1NTEyfHVzLWVhc3QtMXxjb3JlLXNlcnZpY2Vz',
      service: 'retired-api',
      service_id: 'NDgyMTA5Mzc1NTEyfHVzLWVhc3QtMXxjb3JlLXNlcnZpY2VzfHJldGlyZWQtYXBp',
      missing: true,
    }),
  ],
})
export const ROUTE_ALL_MISSING = route({
  id: 7,
  name: 'Old payments channel',
  destination: 'slack',
  target: 'hooks.slack.com',
  matches_nothing: true,
  scope: [{ ...IMAGE_RESIZER_SERVICE, service: 'old-service', missing: true }],
})
export const ROUTE_UNREADABLE = route({
  id: 8,
  name: 'After key rotation',
  destination: 'webhook',
  target: 'hooks.example.com',
  secret_unreadable: true,
})

export const NOTIFICATION_ROUTES: NotificationRoutes = {
  items: [
    ROUTE_PROD_PAGERDUTY,
    ROUTE_CORE_SLACK,
    ROUTE_RESIZER_TELEGRAM,
    ROUTE_STAGING_EMAIL,
    ROUTE_CATCH_ALL,
    ROUTE_WEBHOOK_SIGNED,
    ROUTE_ALL_MISSING,
    ROUTE_UNREADABLE,
  ],
  email_available: true,
  secrets_available: true,
  kinds: NOTIFICATION_KINDS,
}

export const NOTIFICATION_ROUTES_NO_SMTP: NotificationRoutes = { ...NOTIFICATION_ROUTES, email_available: false }
export const NOTIFICATION_ROUTES_NO_KEY: NotificationRoutes = { ...NOTIFICATION_ROUTES, secrets_available: false }
export const NOTIFICATION_ROUTES_EMPTY: NotificationRoutes = { ...NOTIFICATION_ROUTES, items: [] }

export const NOTIFICATION_PREVIEW: NotificationPreview = {
  matches: [
    { service: 'payments-api', cluster: 'core-services', kind: 'service_failing', severity: 'critical', title: 'payments-api: service failing' },
    { service: 'image-resizer', cluster: 'background-jobs', kind: 'restart_loop', severity: 'warning', title: 'image-resizer: restart loop' },
  ],
  match_count: 2,
  sample: false,
  destination: 'slack',
  title: 'payments-api: service failing',
  body: '0 of 3 tasks are running.\ncore-services · homelab-admin / us-east-1',
  json: '',
}

export const NOTIFICATION_PREVIEW_EMPTY: NotificationPreview = {
  matches: [],
  match_count: 0,
  sample: true,
  destination: 'slack',
  title: 'image-resizer: restart loop',
  body: '3 tasks stopped with an error in the last hour.\nbackground-jobs · example-account / us-east-1',
  json: '',
}

export const NOTIFICATION_PREVIEW_WEBHOOK: NotificationPreview = {
  ...NOTIFICATION_PREVIEW,
  destination: 'webhook',
  title: '',
  body: '',
  json: '{\n  "event": "alert.raised",\n  "severity": "critical"\n}',
}

export const NOTIFICATION_DELIVERY_TEST: NotificationDelivery = {
  id: 101,
  at: '2026-11-21T10:05:00Z',
  finished_at: '2026-11-21T10:05:00Z',
  event: 'test',
  alert: 'Test message',
  service: '',
  cluster: '',
  service_id: '',
  route_id: 2,
  route: 'core-services to #core-alerts',
  destination: 'slack',
  target: 'hooks.slack.com',
  status: 'delivered',
  note: 'Delivered in 112 ms',
  held_until: null,
  attempts: 1,
  can_retry: false,
}

function delivery(fields: Partial<NotificationDelivery> & Pick<NotificationDelivery, 'id' | 'status'>): NotificationDelivery {
  return {
    at: '2026-11-21T09:30:00Z',
    finished_at: '2026-11-21T09:30:01Z',
    event: 'opened',
    alert: 'payments-api: service failing',
    service: 'payments-api',
    cluster: 'core-services',
    service_id: 'core-services-payments-api',
    route_id: 2,
    route: 'core-services to #core-alerts',
    destination: 'slack',
    target: 'hooks.slack.com',
    note: '',
    held_until: null,
    attempts: 1,
    can_retry: false,
    ...fields,
  }
}

export const DELIVERY_DELIVERED = delivery({ id: 110, status: 'delivered', note: 'Delivered in 128 ms' })
export const DELIVERY_FAILED = delivery({
  id: 109,
  status: 'failed',
  at: '2026-11-21T09:20:00Z',
  alert: 'image-resizer: restart loop',
  service: 'image-resizer',
  cluster: 'background-jobs',
  service_id: IMAGE_RESIZER_SERVICE.service_id,
  route_id: 3,
  route: 'image-resizer to Telegram',
  destination: 'telegram',
  target: '-1001234567890',
  note: 'HTTP 503 after 3 attempts',
  attempts: 3,
  can_retry: true,
})
export const DELIVERY_HELD = delivery({
  id: 108,
  status: 'held',
  at: '2026-11-21T01:10:00Z',
  finished_at: null,
  alert: 'auth: service degraded',
  service: 'auth',
  held_until: '2026-11-21T07:00:00Z',
  attempts: 0,
})
export const DELIVERY_PENDING = delivery({ id: 111, status: 'pending', finished_at: null, event: 'repeat', attempts: 1, note: 'HTTP 503' })
export const DELIVERY_CANCELLED = delivery({
  id: 107,
  status: 'cancelled',
  at: '2026-11-21T00:50:00Z',
  alert: 'auth: service degraded',
  service: 'auth',
  note: 'Resolved during quiet hours; not sent.',
  attempts: 0,
})

export const NOTIFICATION_DELIVERIES: NotificationDeliveries = {
  items: [DELIVERY_PENDING, DELIVERY_DELIVERED, NOTIFICATION_DELIVERY_TEST, DELIVERY_FAILED, DELIVERY_HELD, DELIVERY_CANCELLED],
  next: null,
  polled_at: '2026-11-21T10:10:00Z',
}
export const NOTIFICATION_DELIVERIES_FAILED: NotificationDeliveries = { ...NOTIFICATION_DELIVERIES, items: [DELIVERY_FAILED] }
export const NOTIFICATION_DELIVERIES_EMPTY: NotificationDeliveries = { ...NOTIFICATION_DELIVERIES, items: [] }

export const NOTIFICATION_SUMMARY: NotificationSummary = { failed_24h: 2, failing_routes: 1 }
export const NOTIFICATION_SUMMARY_CLEAN: NotificationSummary = { failed_24h: 0, failing_routes: 0 }

export const NOTIFICATION_TARGETS: NotificationTargets = {
  accounts: [
    { account_id: '482109375512', name: 'homelab-admin', regions: ['us-east-1', 'eu-west-1', 'eu-central-1'] },
    { account_id: '731022849906', name: 'staging-example', regions: ['us-east-1'] },
  ],
  clusters: [
    { id: CORE_SERVICES_CLUSTER.cluster_id, name: 'core-services', account_id: '482109375512', account_name: 'homelab-admin', region: 'us-east-1' },
    { id: IMAGE_RESIZER_SERVICE.cluster_id, name: 'background-jobs', account_id: '482109375512', account_name: 'homelab-admin', region: 'us-east-1' },
    { id: 'staging-web-cluster', name: 'staging-web', account_id: '731022849906', account_name: 'staging-example', region: 'us-east-1' },
  ],
  services: [
    {
      id: 'core-services-payments-api',
      name: 'payments-api',
      cluster: 'core-services',
      cluster_id: CORE_SERVICES_CLUSTER.cluster_id,
      account_id: '482109375512',
      account_name: 'homelab-admin',
      region: 'us-east-1',
    },
    {
      id: IMAGE_RESIZER_SERVICE.service_id,
      name: 'image-resizer',
      cluster: 'background-jobs',
      cluster_id: IMAGE_RESIZER_SERVICE.cluster_id,
      account_id: '482109375512',
      account_name: 'homelab-admin',
      region: 'us-east-1',
    },
    {
      id: 'staging-web-web',
      name: 'web',
      cluster: 'staging-web',
      cluster_id: 'staging-web-cluster',
      account_id: '731022849906',
      account_name: 'staging-example',
      region: 'us-east-1',
    },
  ],
}
