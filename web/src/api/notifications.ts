// Types and hooks for the alert notification endpoints. They mirror internal/api/notifications_dto.go.

export type Destination = 'slack' | 'telegram' | 'pagerduty' | 'webhook' | 'email'
export type Severity = 'warning' | 'critical'
export type SelectorKind = 'account' | 'cluster' | 'service' | 'aws' | 'harbormaster'

/** One scope entry of a route. A request reads kind plus the ids; the names and `missing` are output only. */
export interface NotificationSelector {
  kind: SelectorKind
  /** account; also filled for cluster and service. */
  account_id: string
  account_name: string
  region: string
  /** cluster; also filled for service. */
  cluster_id: string
  cluster: string
  /** service. */
  service_id: string
  service: string
  /** aws. */
  key: string
  value: string
  /** harbormaster. */
  tag_id: number
  tag_name: string
  /** The target is gone: the account is not connected, or the cluster or service is not in the snapshot. */
  missing: boolean
}

export interface NotificationRoute {
  id: number
  name: string
  enabled: boolean
  destination: Destination
  /** Display only: the label, the host of the stored URL, the chat or the recipients. */
  target: string
  label: string
  recipients: string[]
  /** "us" or "eu" for PagerDuty, empty otherwise. */
  pagerduty_region: string
  chat_id: string
  thread_id: number | null
  /** A secret is stored (never returned). */
  secret_set: boolean
  /** A signing secret is stored. */
  signed: boolean
  /** The stored secret no longer opens (the session key changed); the destination must be entered again. */
  secret_unreadable: boolean
  min_severity: Severity
  kinds: string[]
  repeat_seconds: number
  quiet_enabled: boolean
  quiet_timezone: string
  notify_resolved: boolean
  /** Alerts from any of these; empty means everything. */
  scope: NotificationSelector[]
  /** Only when no other enabled route matches the alert. */
  fallback: boolean
  /** The route has scope entries and every one is missing. */
  matches_nothing: boolean
  /** Its last three deliveries all failed. */
  failing: boolean
  version: number
  updated_at: string
}

export interface NotificationKind {
  kind: string
  label: string
}

export interface NotificationRoutes {
  items: NotificationRoute[]
  email_available: boolean
  secrets_available: boolean
  kinds: NotificationKind[]
}

export interface NotificationMatch {
  service: string
  cluster: string
  kind: string
  severity: Severity
  title: string
}

export interface NotificationPreview {
  matches: NotificationMatch[]
  match_count: number
  /** No open alert matched, so the built-in sample was rendered. */
  sample: boolean
  destination: Destination
  title: string
  body: string
  json: string
}

export type DeliveryStatus = 'pending' | 'held' | 'delivered' | 'failed' | 'cancelled'

export interface NotificationDelivery {
  id: number
  at: string
  finished_at: string | null
  event: 'opened' | 'repeat' | 'resolved' | 'test'
  /** The alert title, or "Test message". */
  alert: string
  service: string
  cluster: string
  service_id: string
  route_id: number | null
  route: string
  destination: Destination
  target: string
  status: DeliveryStatus
  note: string
  held_until: string | null
  attempts: number
  can_retry: boolean
}

export interface NotificationTargetAccount {
  account_id: string
  name: string
  regions: string[]
}

export interface NotificationTargetCluster {
  id: string
  name: string
  account_id: string
  account_name: string
  region: string
}

export interface NotificationTargetService {
  id: string
  name: string
  cluster: string
  cluster_id: string
  account_id: string
  account_name: string
  region: string
}

export interface NotificationDeliveries {
  items: NotificationDelivery[]
  next: string | null
  polled_at: string
}

/** What the failed-deliveries banner and the Settings badge show. */
export interface NotificationSummary {
  failed_24h: number
  failing_routes: number
}

/** Everything the scope picker offers except tags (those come from the off-hours tags endpoint). */
export interface NotificationTargets {
  accounts: NotificationTargetAccount[]
  clusters: NotificationTargetCluster[]
  services: NotificationTargetService[]
}
