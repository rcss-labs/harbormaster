// Wire types for the image security endpoints and the hooks that read and write them.
// The types mirror internal/api/security_dto.go, security_write.go and security_gate.go.
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from './client'
import { queryString, type Filters } from './hooks'
import type { TagDTO } from './types'

// ---- Shared values ----

export type Severity = 'critical' | 'high' | 'medium' | 'low' | 'negligible' | 'unknown'

/** Stored state of an image's scan. */
export type ScanImageStatus = 'queued' | 'scanning' | 'scanned' | 'failed'

/** Freshness of the vulnerability database: fresh, warn, stale, or none when it was never built. */
export type DBFreshness = 'fresh' | 'warn' | 'stale' | 'none'

/** Scan state of a service's latest request, as ScanStatusDTO reports it. */
export type ScanState =
  | 'none'
  | 'queued'
  | 'scanning'
  | 'scanned'
  | 'failed'
  | 'db_stale'
  | 'refreshing_db'
  | 'unsupported'

/** Pre-deploy gate state of one changed image, as ScanGateImageDTO reports it. */
export type ScanGateState =
  | 'pass'
  | 'critical'
  | 'unsupported'
  | 'failed'
  | 'db_stale'
  | 'refreshing_db'
  | 'queued'
  | 'scanning'

export interface SeverityCounts {
  critical: number
  high: number
  medium: number
  low: number
  negligible: number
  unknown: number
}

// ---- GET /api/security ----

export interface SecurityDB {
  built_at: string | null
  freshness: DBFreshness | ''
  refreshing: boolean
  last_refresh_error: string
}

export interface SecurityScanner {
  running_runs: number
  running_images: number
  queued: number
  last_success_at: string | null
  last_error: string
  failing_since: string | null
}

/** One service container that asked for an image to be scanned. Only services the caller can see are listed. */
export interface SecurityRequest {
  service_id: string
  service: string
  cluster: string
  container: string
  tag: string
  source: string
  by: string
  at: string
  permission_tag: TagDTO
  can_accept: boolean
}

export interface SecurityCan {
  accept: boolean
  rescan: boolean
  set_base: boolean
}

/**
 * How the base image of an image's repository relates to this image. Matched means the base layers are a prefix of this
 * image's layers, so findings carry an origin; mismatch and error carry the server's explanation in message.
 */
export type BaseImageState = 'none' | 'pending' | 'matched' | 'mismatch' | 'error'

export interface SecurityBaseImage {
  ref: string
  scanned_ref: string
  state: BaseImageState
  matched_layers: number
  total_layers: number
  message: string
}

/** One requested image, as the Security page lists it. */
export interface SecurityImage {
  digest: string
  image: string
  repository: string
  tag: string
  registry_account_id: string
  registry_region: string
  requests: SecurityRequest[]
  status: ScanImageStatus
  failure_code: string
  failure_detail: string
  failure_action: string
  platform: string
  scanned_at: string | null
  matched_db_built_at: string | null
  syft_version: string
  packages: number | null
  truncated: boolean
  open: SeverityCounts
  accepted: number
  last_requested_at: string
  base_image: SecurityBaseImage
  can: SecurityCan
}

export interface SecurityResponse {
  enabled: boolean
  hint: string
  db: SecurityDB
  scanner: SecurityScanner
  note: string
  images: SecurityImage[]
  polled_at: string
}

// ---- GET /api/security/images/{digest} ----

export interface SecurityAcceptance {
  id: number
  service_id: string
  service: string
  by: string
  reason: string
  created_at: string
  expires_at: string
  can_revoke: boolean
}

export interface SecurityFinding {
  vuln_id: string
  severity: Severity
  cvss: number | null
  package: string
  version: string
  type: string
  location: string
  fixed_version: string
  fix_state: string
  data_source: string
  url: string
  first_seen_at: string
  /** Which layers added the package. Empty unless the base image state is matched. */
  origin: 'base' | 'app' | ''
  acceptances: SecurityAcceptance[]
  accepted_everywhere: boolean
}

export interface SecurityImageDetail extends SecurityImage {
  enabled: boolean
  hint: string
  findings: SecurityFinding[]
}

// ---- GET and PUT /api/security/services/{serviceId} ----

export interface SecurityService {
  enabled: boolean
  pre_deploy_scan: boolean
  can_change_setting: boolean
  images: SecurityImage[]
}

// ---- Scan requests and status ----

/** GET /api/security/services/{serviceId}/scans, or the answer to POST scans and POST rescan. */
export interface ScanStatus {
  enabled: boolean
  state: ScanState
  image: SecurityImage | null
  message: string
}

/** Pre-deploy gate row for one changed image. Ref is the digest resolved now, or the image reference when none resolved. */
export interface ScanGateImage {
  container: string
  image: string
  ref: string
  digest: string
  state: ScanGateState
  open: SeverityCounts
  accepted: number
  message: string
}

/** The scan_gate member of the 409 that a change set or registration answers while the gate is paused. */
export interface ScanGate {
  message: string
  images: ScanGateImage[]
}

/** The scan_gate member of a change set or registration request: the caller's "Deploy anyway". */
export interface ScanGateRequest {
  deploy_anyway: boolean
  images: { container: string; ref: string }[]
}

// ---- Write payloads ----

export interface AcceptRiskRequest {
  vuln_id: string
  package_name: string
  package_version: string
  reason: string
  days: number
  service_ids?: string[]
}

export interface AcceptPackageRiskRequest {
  package_name: string
  reason: string
  days: number
  service_ids?: string[]
}

export interface AcceptRiskResponse {
  ids: number[]
}

export interface ScanRequestBody {
  container: string
  tag: string
}

/** What a scan status query asks about: a container and tag of the service, or a digest. */
export type ScanTarget = { container: string; tag: string } | { digest: string }

// ---- Hooks ----

/** Scan states that still change on the server, so their status is polled. */
const ACTIVE_SCAN_STATES: readonly ScanState[] = ['queued', 'scanning', 'refreshing_db']
const SCAN_POLL_MS = 3000

export function useSecurity(filters: Filters = {}) {
  return useQuery({
    queryKey: ['security', filters],
    queryFn: () => api.get<SecurityResponse>(`/api/security${queryString({ ...filters })}`),
    refetchInterval: 30_000,
    refetchIntervalInBackground: false,
  })
}

export function useSecurityImage(digest: string) {
  return useQuery({
    queryKey: ['security', 'image', digest],
    queryFn: () => api.get<SecurityImageDetail>(`/api/security/images/${digest}`),
    enabled: digest !== '',
  })
}

export function useSecurityService(serviceId: string) {
  return useQuery({
    queryKey: ['security', 'service', serviceId],
    queryFn: () => api.get<SecurityService>(`/api/security/services/${serviceId}`),
    enabled: serviceId !== '',
  })
}

/** The latest scan of a container and tag, or of a digest. Polls every 3 s while the scan is still moving. */
export function useScanStatus(serviceId: string, target: ScanTarget, enabled: boolean) {
  const query = 'digest' in target ? { digest: target.digest } : { container: target.container, tag: target.tag }
  return useQuery({
    queryKey: ['security', 'scan', serviceId, query],
    queryFn: () => api.get<ScanStatus>(`/api/security/services/${serviceId}/scans${queryString(query)}`),
    enabled,
    refetchInterval: (poll) => (poll.state.data && ACTIVE_SCAN_STATES.includes(poll.state.data.state) ? SCAN_POLL_MS : false),
    refetchIntervalInBackground: false,
  })
}

/** "Scan for vulnerabilities" for one container and tag of a service. */
export function useRequestScan(serviceId: string) {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (body: ScanRequestBody) => api.post<ScanStatus>(`/api/security/services/${serviceId}/scans`, body),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['security'] }),
  })
}

/** Turns "Scan Image for Vulnerabilities Before Deploy" on or off for a service. */
export function useSetPreDeployScan(serviceId: string) {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (preDeployScan: boolean) =>
      api.put<SecurityService>(`/api/security/services/${serviceId}/setting`, { pre_deploy_scan: preDeployScan }),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['security'] }),
  })
}

export function useAcceptRisk(digest: string) {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (body: AcceptRiskRequest) => api.post<AcceptRiskResponse>(`/api/security/images/${digest}/acceptances`, body),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['security'] }),
  })
}

/** Accepts every vulnerability of one package, in any version, that a chosen service still counts. */
export function useAcceptPackageRisk(digest: string) {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (body: AcceptPackageRiskRequest) => api.post<AcceptRiskResponse>(`/api/security/images/${digest}/package-acceptances`, body),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['security'] }),
  })
}

export function useRevokeAcceptance() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (acceptanceId: number) => api.del(`/api/security/acceptances/${acceptanceId}`),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['security'] }),
  })
}

export function useRescanImage(digest: string) {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: () => api.post<ScanStatus>(`/api/security/images/${digest}/rescan`),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['security'] }),
  })
}

/** Sets the base image of the image's repository; an empty ref clears it. The server queues a rescan after saving. */
export function useSetBaseImage(digest: string) {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (baseRef: string) =>
      api.put<SecurityImageDetail>(`/api/security/images/${digest}/base-image`, { base_ref: baseRef }),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['security'] }),
  })
}
