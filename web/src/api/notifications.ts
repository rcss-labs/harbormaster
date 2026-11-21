// Types and hooks for the alert notification endpoints. They mirror internal/api/notifications_dto.go.
import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from './client'

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
  /** Failing services that make a cluster or account entry notify (1 to 500); null for service and tag entries. */
  threshold: number | null
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
  /** How long service_failing and service_degraded must stay open before they notify or count; 0 means no wait. */
  settle_seconds: number
}

export interface NotificationMatch {
  service: string
  cluster: string
  kind: string
  severity: Severity
  title: string
}

/** A cluster or account entry of the draft: open when its threshold is met, else listed with what is failing. */
export interface NotificationGroupPreview {
  kind: 'cluster' | 'account'
  label: string
  cluster_id: string
  account_id: string
  region: string
  threshold: number
  failing: number
  total: number
  open: boolean
  /** The account cannot be read, which opens its account entry whatever the threshold. */
  unreachable: boolean
  /** Failing service names, at most 10. */
  services: string[]
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
  /** Open groups first, then entries below their threshold. */
  groups: NotificationGroupPreview[]
  /** What the route would send right now: per-alert matches plus open groups. */
  notification_count: number
  /** Alerts still inside the settle time that would otherwise match or count. */
  pending: number
}

export type DeliveryStatus = 'pending' | 'held' | 'delivered' | 'failed' | 'cancelled'

export interface NotificationDelivery {
  id: number
  at: string
  finished_at: string | null
  event: 'opened' | 'repeat' | 'update' | 'resolved' | 'test'
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
  /** service, cluster or account; empty for a test message. */
  scope: 'service' | 'cluster' | 'account' | ''
  /** Link a row to its cluster or account (an aggregated row has no service). */
  cluster_id: string
  account_id: string
  region: string
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

/** One scope entry in a request. Cluster and account entries carry a threshold; service and tag entries do not. */
export type NotificationSelectorInput =
  | { kind: 'account'; account_id: string; region?: string; threshold: number }
  | { kind: 'cluster'; cluster_id: string; threshold: number }
  | { kind: 'service'; service_id: string }
  | { kind: 'aws'; key: string; value: string }
  | { kind: 'harbormaster'; tag_id: number }

/** The body of create, update and preview. A secret that is left out keeps the stored one on update. */
export interface NotificationRouteInput {
  name: string
  enabled: boolean
  destination: Destination
  label: string
  recipients: string[]
  pagerduty_region: string
  chat_id: string
  thread_id: number | null
  url?: string
  routing_key?: string
  /** An empty string removes the stored signing secret. */
  signing_secret?: string
  bot_token?: string
  min_severity: Severity
  kinds: string[]
  repeat_seconds: number
  quiet_enabled: boolean
  quiet_timezone: string
  notify_resolved: boolean
  scope: NotificationSelectorInput[]
  fallback: boolean
  /** Required on update. */
  version?: number
}

export interface NotificationPreviewRequest extends NotificationRouteInput {
  /** The route being edited, so a catch-all draft does not count it as covering an alert. */
  route_id?: number
}

const ROUTES_KEY = ['notifications', 'routes'] as const

export function useNotificationRoutes() {
  return useQuery({ queryKey: ROUTES_KEY, queryFn: () => api.get<NotificationRoutes>('/api/notifications/routes') })
}

export function useNotificationTargets() {
  return useQuery({
    queryKey: ['notifications', 'targets'],
    queryFn: () => api.get<NotificationTargets>('/api/notifications/targets'),
    staleTime: 60_000,
  })
}

function useInvalidateRoutes() {
  const queryClient = useQueryClient()
  return () =>
    Promise.all([
      queryClient.invalidateQueries({ queryKey: ROUTES_KEY }),
      queryClient.invalidateQueries({ queryKey: ['notifications', 'summary'] }),
    ])
}

export function useCreateNotificationRoute() {
  const invalidate = useInvalidateRoutes()
  return useMutation({
    mutationFn: (body: NotificationRouteInput) => api.post<NotificationRoute>('/api/notifications/routes', body),
    onSuccess: invalidate,
  })
}

export function useUpdateNotificationRoute(id: number) {
  const invalidate = useInvalidateRoutes()
  return useMutation({
    mutationFn: (body: NotificationRouteInput) => api.put<NotificationRoute>(`/api/notifications/routes/${id}`, body),
    onSuccess: invalidate,
  })
}

export function useDeleteNotificationRoute() {
  const invalidate = useInvalidateRoutes()
  return useMutation({
    mutationFn: (id: number) => api.del(`/api/notifications/routes/${id}`),
    onSuccess: invalidate,
  })
}

/** Renders what a draft would send and counts what it would send right now; nothing is written. */
export function useNotificationPreview() {
  return useMutation({ mutationFn: (body: NotificationPreviewRequest) => api.post<NotificationPreview>('/api/notifications/preview', body) })
}

export function useSendNotificationTest(id: number) {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: () => api.post<NotificationDelivery>(`/api/notifications/routes/${id}/test`),
    onSuccess: () =>
      Promise.all([
        queryClient.invalidateQueries({ queryKey: ['notifications', 'deliveries'] }),
        queryClient.invalidateQueries({ queryKey: ['notifications', 'summary'] }),
        queryClient.invalidateQueries({ queryKey: ROUTES_KEY }),
      ]),
  })
}

/** The delivery log, newest first, a page at a time; it refreshes while the log is on screen. */
export function useNotificationDeliveries(status: 'all' | 'failed', enabled = true) {
  return useInfiniteQuery({
    queryKey: ['notifications', 'deliveries', status],
    queryFn: ({ pageParam }) => {
      const params = new URLSearchParams()
      if (status === 'failed') params.set('status', 'failed')
      if (pageParam) params.set('cursor', pageParam)
      const query = params.toString()
      return api.get<NotificationDeliveries>(`/api/notifications/deliveries${query ? `?${query}` : ''}`)
    },
    initialPageParam: '',
    getNextPageParam: (last) => last.next ?? undefined,
    enabled,
    refetchInterval: enabled ? 5000 : false,
  })
}

export function useRetryNotificationDelivery() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (id: number) => api.post<NotificationDelivery>(`/api/notifications/deliveries/${id}/retry`),
    onSuccess: () =>
      Promise.all([
        queryClient.invalidateQueries({ queryKey: ['notifications', 'deliveries'] }),
        queryClient.invalidateQueries({ queryKey: ['notifications', 'summary'] }),
      ]),
  })
}

/** The failed-deliveries banner and the Settings badge. `enabled` is false for non-admins, who may not read it. */
export function useNotificationSummary(enabled: boolean) {
  return useQuery({
    queryKey: ['notifications', 'summary'],
    queryFn: () => api.get<NotificationSummary>('/api/notifications/summary'),
    enabled,
    refetchInterval: 60_000,
  })
}
