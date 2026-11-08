// Image security fixtures shaped like the Go DTOs in internal/api/security_dto.go and security_gate.go, and the MSW
// handlers that answer the security endpoints with them. Finding ids are CVE-0000-NNNN; the copy in failure and gate
// messages is the server's own.
import { http, HttpResponse } from 'msw'
import type {
  ScanGate,
  ScanStatus,
  SecurityAcceptance,
  SecurityFinding,
  SecurityImage,
  SecurityImageDetail,
  SecurityRequest,
  SecurityResponse,
  SecurityService,
  SeverityCounts,
} from '../api/security'
import { FIXTURE_NOW, SERVICE_NOTIFICATIONS, SERVICE_PAYMENTS, TAG_PROD, TAG_STAGING } from './fixtures'

const NO_COUNTS: SeverityCounts = { critical: 0, high: 0, medium: 0, low: 0, negligible: 0, unknown: 0 }
/** The zero time the server writes for a field it did not set. */
const ZERO_TIME = '0001-01-01T00:00:00Z'
const EXPIRES_AT = '2026-11-30T09:00:00Z'

const SCANNING_OFF_HINT =
  'Image scanning is off. Apply the harbormaster module with enable_image_scanning = true and the harbormaster-access module with scanner_task_role_arn.'
const SECURITY_NOTE = "Only images requested from a service's Deploy tab are scanned."
const TIMEOUT_COPY = 'The scan did not finish in time, even with a larger scanner task. Rescan to try again.'
const ECR_TIMEOUT_COPY = 'The image could not be checked in ECR (timeout). You can deploy anyway.'
const GATE_MESSAGE = 'Some images have not passed the pre-deploy scan. You can deploy anyway.'

export const DIGEST_EDGE = 'sha256:5387484a6aa4be14a297874efb5b44b7de33223df13406187623916ac360f394'
export const DIGEST_QUEUED = 'sha256:f88e3013d2a7de8085066d8490f3e37234c4ed6f97fb522ed08255994b2f1859'
export const DIGEST_FAILED = 'sha256:4efa86c4588310059fd899b463d5849779d2b1bc9915744ea7ee0d2b815715c9'
export const DIGEST_GATE_CRITICAL = 'sha256:0cda7255429282b5a0b417d5827514ba385b397b83c173f76568d817c05ecddd'

const REGISTRY = '482109375512.dkr.ecr.us-east-1.amazonaws.com'

const acceptedByPayments: SecurityAcceptance = {
  id: 9001,
  service_id: SERVICE_PAYMENTS.id,
  service: 'payments-api',
  by: 'sam',
  reason: 'Only reachable from the private subnet; patch scheduled for next sprint.',
  created_at: '2026-10-31T09:00:00Z',
  expires_at: EXPIRES_AT,
  can_revoke: true,
}

const EDGE_FINDINGS: SecurityFinding[] = [
  {
    vuln_id: 'CVE-0000-0001',
    severity: 'critical',
    cvss: 9.8,
    package: 'openssl',
    version: '3.0.2-0ubuntu1',
    type: 'deb',
    location: '/usr/lib/x86_64-linux-gnu/libssl.so.3',
    fixed_version: '3.0.15-1',
    fix_state: 'fixed',
    data_source: 'nvd:cpe',
    url: 'https://nvd.nist.gov/vuln/detail/CVE-0000-0001',
    first_seen_at: '2026-10-28T06:00:00Z',
    acceptances: [acceptedByPayments],
    accepted_everywhere: false,
  },
  {
    vuln_id: 'CVE-0000-0002',
    severity: 'high',
    cvss: 7.5,
    package: 'libxml2',
    version: '2.9.14',
    type: 'deb',
    location: '/usr/lib/x86_64-linux-gnu/libxml2.so.2',
    fixed_version: '',
    fix_state: 'not-fixed',
    data_source: 'nvd:cpe',
    url: 'https://nvd.nist.gov/vuln/detail/CVE-0000-0002',
    first_seen_at: '2026-10-28T06:00:00Z',
    acceptances: [],
    accepted_everywhere: false,
  },
  {
    vuln_id: 'CVE-0000-0003',
    severity: 'medium',
    cvss: 5.3,
    package: 'zlib',
    version: '1.2.11',
    type: 'deb',
    location: '/usr/lib/x86_64-linux-gnu/libz.so.1',
    fixed_version: '1.2.13',
    fix_state: 'fixed',
    data_source: 'nvd:cpe',
    url: 'https://nvd.nist.gov/vuln/detail/CVE-0000-0003',
    first_seen_at: '2026-10-29T06:00:00Z',
    acceptances: [],
    accepted_everywhere: false,
  },
]

// Requested by two services: payments-api from its Deploy tab and notifications through the pre-deploy gate.
const EDGE_REQUESTS: SecurityRequest[] = [
  {
    service_id: SERVICE_PAYMENTS.id,
    service: 'payments-api',
    cluster: 'core-services',
    container: 'app',
    tag: '3.2.0',
    source: 'manual',
    by: 'sam',
    at: '2026-10-31T11:30:00Z',
    permission_tag: TAG_PROD,
    can_accept: true,
  },
  {
    service_id: SERVICE_NOTIFICATIONS.id,
    service: 'notifications',
    cluster: 'core-services',
    container: 'worker',
    tag: '3.2.0',
    source: 'pre_deploy',
    by: 'sam',
    at: '2026-10-31T11:35:00Z',
    permission_tag: TAG_STAGING,
    can_accept: false,
  },
]

const IMAGE_EDGE: SecurityImage = {
  digest: DIGEST_EDGE,
  image: `${REGISTRY}/platform-base:3.2.0`,
  repository: 'platform-base',
  tag: '3.2.0',
  registry_account_id: '482109375512',
  registry_region: 'us-east-1',
  requests: EDGE_REQUESTS,
  status: 'scanned',
  failure_code: '',
  failure_detail: '',
  failure_action: '',
  platform: 'linux/amd64',
  scanned_at: '2026-10-31T11:40:00Z',
  matched_db_built_at: '2026-10-30T20:00:00Z',
  syft_version: '1.9.0',
  packages: 214,
  truncated: false,
  open: { ...NO_COUNTS, critical: 1, high: 1, medium: 1 },
  accepted: 1,
  last_requested_at: '2026-10-31T11:35:00Z',
  can: { accept: true, rescan: true },
}

export const SECURITY_IMAGE_EDGE: SecurityImageDetail = {
  ...IMAGE_EDGE,
  enabled: true,
  hint: '',
  findings: EDGE_FINDINGS,
}

const IMAGE_QUEUED: SecurityImage = {
  ...IMAGE_EDGE,
  digest: DIGEST_QUEUED,
  image: `${REGISTRY}/payments-api:1.18.2`,
  repository: 'payments-api',
  tag: '1.18.2',
  requests: [EDGE_REQUESTS[0]],
  status: 'queued',
  scanned_at: null,
  matched_db_built_at: null,
  syft_version: '',
  packages: null,
  open: NO_COUNTS,
  accepted: 0,
}

const IMAGE_FAILED: SecurityImage = {
  ...IMAGE_QUEUED,
  digest: DIGEST_FAILED,
  image: `${REGISTRY}/payments-api:1.17.0`,
  tag: '1.17.0',
  status: 'failed',
  failure_code: 'timeout',
  failure_action: TIMEOUT_COPY,
}

const DB_FRESH = { built_at: '2026-10-30T20:00:00Z', freshness: 'fresh' as const, refreshing: false, last_refresh_error: '' }
const SCANNER_BUSY = {
  running_runs: 1,
  running_images: 1,
  queued: 1,
  last_success_at: '2026-10-31T11:40:00Z',
  last_error: '',
  failing_since: null,
}

export const SECURITY: SecurityResponse = {
  enabled: true,
  hint: '',
  db: DB_FRESH,
  scanner: SCANNER_BUSY,
  note: SECURITY_NOTE,
  images: [IMAGE_EDGE],
  polled_at: FIXTURE_NOW,
}

export const SECURITY_STALE: SecurityResponse = {
  ...SECURITY,
  db: {
    built_at: '2026-10-20T08:00:00Z',
    freshness: 'stale',
    refreshing: false,
    last_refresh_error: 'Refresh failed: the bucket did not accept the write.',
  },
}

/** Scanning is off: the server answers only the switch, a hint and the note. */
export const SECURITY_OFF: SecurityResponse = {
  enabled: false,
  hint: SCANNING_OFF_HINT,
  db: { built_at: null, freshness: '', refreshing: false, last_refresh_error: '' },
  scanner: { running_runs: 0, running_images: 0, queued: 0, last_success_at: null, last_error: '', failing_since: null },
  note: SECURITY_NOTE,
  images: [],
  polled_at: ZERO_TIME,
}

export const SECURITY_SERVICE_EDGE: SecurityService = {
  enabled: true,
  pre_deploy_scan: true,
  can_change_setting: true,
  images: [IMAGE_EDGE],
}

export const SECURITY_SERVICE_OFF: SecurityService = {
  enabled: false,
  pre_deploy_scan: false,
  can_change_setting: false,
  images: [],
}

export const SCAN_STATUS_NONE: ScanStatus = { enabled: true, state: 'none', image: null, message: '' }

export const SCAN_STATUS_QUEUED: ScanStatus = { enabled: true, state: 'queued', image: IMAGE_QUEUED, message: '' }

export const SCAN_STATUS_SCANNED: ScanStatus = { enabled: true, state: 'scanned', image: IMAGE_EDGE, message: '' }

export const SCAN_STATUS_FAILED: ScanStatus = { enabled: true, state: 'failed', image: IMAGE_FAILED, message: TIMEOUT_COPY }

/** The pre-deploy gate paused on a scanned image with a critical open finding. */
export const SCAN_GATE_CRITICAL: ScanGate = {
  message: GATE_MESSAGE,
  images: [
    {
      container: 'worker',
      image: `${REGISTRY}/notifications:2.4.0`,
      ref: DIGEST_GATE_CRITICAL,
      digest: DIGEST_GATE_CRITICAL,
      state: 'critical',
      open: { ...NO_COUNTS, critical: 1 },
      accepted: 0,
      message: '',
    },
  ],
}

/** The pre-deploy gate paused because ECR did not answer in time, so the row has no digest. */
export const SCAN_GATE_FAILED: ScanGate = {
  message: GATE_MESSAGE,
  images: [
    {
      container: 'app',
      image: `${REGISTRY}/platform-base:3.2.1`,
      ref: `${REGISTRY}/platform-base:3.2.1`,
      digest: '',
      state: 'failed',
      open: NO_COUNTS,
      accepted: 0,
      message: ECR_TIMEOUT_COPY,
    },
  ],
}

/** The security response the scenario selects for the page-level read. */
export type SecurityScenario = 'on' | 'stale' | 'off'

let scenario: SecurityScenario = 'on'

/** Chooses what GET /api/security and GET /api/security/services/{id} answer. Reset to 'on' between tests. */
export function setSecurityScenario(next: SecurityScenario): void {
  scenario = next
}

const securityFor = (current: SecurityScenario): SecurityResponse =>
  current === 'off' ? SECURITY_OFF : current === 'stale' ? SECURITY_STALE : SECURITY

const notFound = () => HttpResponse.json({ error: { code: 'not_found', message: 'Not found.' } }, { status: 404 })

/** Default handlers for the security endpoints; msw.ts spreads them into the default set. */
export const securityHandlers = [
  http.get('/api/security', () => HttpResponse.json(securityFor(scenario))),
  http.get('/api/security/images/:digest', ({ params }) =>
    params.digest === DIGEST_EDGE ? HttpResponse.json(SECURITY_IMAGE_EDGE) : notFound(),
  ),
  http.get('/api/security/services/:serviceId', ({ params }) => {
    if (params.serviceId !== SERVICE_PAYMENTS.id) return notFound()
    return HttpResponse.json(scenario === 'off' ? SECURITY_SERVICE_OFF : SECURITY_SERVICE_EDGE)
  }),
  http.put('/api/security/services/:serviceId/setting', async ({ request }) => {
    const body = (await request.json()) as { pre_deploy_scan: boolean }
    return HttpResponse.json({ ...SECURITY_SERVICE_EDGE, pre_deploy_scan: body.pre_deploy_scan })
  }),
  http.post('/api/security/services/:serviceId/scans', () => HttpResponse.json(SCAN_STATUS_QUEUED)),
  http.get('/api/security/services/:serviceId/scans', () => HttpResponse.json(SCAN_STATUS_NONE)),
  http.post('/api/security/images/:digest/acceptances', async ({ request }) => {
    const body = (await request.json()) as { service_ids?: string[] }
    const ids = (body.service_ids ?? [SERVICE_PAYMENTS.id]).map((_, index) => 9100 + index)
    return HttpResponse.json({ ids }, { status: 201 })
  }),
  http.delete('/api/security/acceptances/:id', () => new HttpResponse(null, { status: 204 })),
  http.post('/api/security/images/:digest/rescan', () => HttpResponse.json(SCAN_STATUS_QUEUED, { status: 202 })),
]
