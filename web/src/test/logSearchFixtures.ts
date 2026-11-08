// MSW fixtures for the Log search routes. All of them are checked against the Go contract in
// contract/contract.test.ts.
import { http, HttpResponse } from 'msw'
import type { ListDTO } from '../api/types'
import type { LogSearchResultDTO, LogSearchServiceDTO, SavedLogSearchDTO } from '../api/logSearch'
import { FIXTURE_NOW } from './fixtures'

export const LOG_SEARCH_SERVICES: ListDTO<LogSearchServiceDTO> = {
  items: [
    { id: 'svc-payments', name: 'payments', cluster: 'core', account_id: 'acc-1', account_name: 'admin', region: 'eu-west-1' },
    { id: 'svc-catalog', name: 'catalog', cluster: 'core', account_id: 'acc-1', account_name: 'admin', region: 'eu-west-1' },
  ],
  next: null,
  polled_at: FIXTURE_NOW,
}

const okTarget = { account_id: 'acc-1', account_name: 'admin', region: 'eu-west-1', state: 'ok' as const, message: '', bytes_scanned: 1_503_238_553, records_matched: 3 }

export const LOG_SEARCH_RESULT: LogSearchResultDTO = {
  rows: [
    { t: '2026-11-08T10:00:03.250Z', service_id: 'svc-payments', container: 'app', task_id: 'a1b2c3d4e5f6', message: 'ERROR card declined', message_truncated: false },
    { t: '2026-11-08T10:00:02.100Z', service_id: 'svc-catalog', container: 'app', task_id: 'f6e5d4c3b2a1', message: 'ERROR upstream timeout', message_truncated: false },
    { t: '2026-11-08T10:00:01.000Z', service_id: 'svc-payments', container: 'app', task_id: 'a1b2c3d4e5f6', message: 'ERROR card declined', message_truncated: false },
  ],
  records_matched: 3,
  bytes_scanned: 1_503_238_553,
  capped: false,
  cost_usd: 0.007,
  duration_ms: 2400,
  from: '2026-11-08T09:00:00Z',
  to: '2026-11-08T10:00:00Z',
  services: [
    { id: 'svc-payments', name: 'payments', state: 'ok', message: '' },
    { id: 'svc-catalog', name: 'catalog', state: 'ok', message: '' },
  ],
  targets: [okTarget],
}

export const LOG_SEARCH_RESULT_CAPPED: LogSearchResultDTO = {
  ...LOG_SEARCH_RESULT,
  capped: true,
  records_matched: 48_210,
}

const NO_PERMISSION_MESSAGE =
  'The harbormaster-access role in admin cannot run Logs Insights queries. Update the access module to the latest version.'

export const LOG_SEARCH_RESULT_NO_PERMISSION: LogSearchResultDTO = {
  ...LOG_SEARCH_RESULT,
  rows: [],
  records_matched: 0,
  bytes_scanned: 0,
  cost_usd: 0,
  services: [
    { id: 'svc-payments', name: 'payments', state: 'target_failed', message: NO_PERMISSION_MESSAGE },
    { id: 'svc-catalog', name: 'catalog', state: 'target_failed', message: NO_PERMISSION_MESSAGE },
  ],
  targets: [{ ...okTarget, state: 'no_permission', message: NO_PERMISSION_MESSAGE, bytes_scanned: 0, records_matched: 0 }],
}

export const SAVED_LOG_SEARCHES: ListDTO<SavedLogSearchDTO> = {
  items: [
    {
      id: 'saved-1',
      name: 'Payment errors',
      query: 'filter @message like /ERROR/',
      service_ids: ['svc-payments'],
      hidden_services: 1,
      range: '1h',
      from: null,
      to: null,
      updated_at: FIXTURE_NOW,
    },
    {
      id: 'saved-2',
      name: 'Morning checkout',
      query: '',
      service_ids: ['svc-catalog'],
      hidden_services: 0,
      range: 'custom',
      from: '2026-11-08T09:00:00Z',
      to: '2026-11-08T10:00:00Z',
      updated_at: FIXTURE_NOW,
    },
  ],
  next: null,
  polled_at: FIXTURE_NOW,
}

export const logSearchHandlers = [
  http.get('/api/log-search/services', () => HttpResponse.json(LOG_SEARCH_SERVICES)),
  http.post('/api/log-search', () => HttpResponse.json(LOG_SEARCH_RESULT)),
  http.get('/api/log-search/saved', () => HttpResponse.json(SAVED_LOG_SEARCHES)),
  http.post('/api/log-search/saved', async ({ request }) => {
    const body = (await request.json()) as Omit<SavedLogSearchDTO, 'id' | 'hidden_services' | 'updated_at'>
    return HttpResponse.json({ ...SAVED_LOG_SEARCHES.items[0], ...body, id: 'saved-3', hidden_services: 0, updated_at: FIXTURE_NOW })
  }),
  http.delete('/api/log-search/saved/:id', () => new HttpResponse(null, { status: 204 })),
]
