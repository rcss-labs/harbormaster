// MSW fixtures for GET /api/config-search. All of them are checked against the Go contract in
// contract/contract.test.ts.
import { http, HttpResponse } from 'msw'
import type { ConfigContainerDTO, ConfigSearchItemDTO, ConfigSearchResponse } from '../api/configSearch'
import { FIXTURE_NOW } from './fixtures'

const PROD_TAG = { id: 3, name: 'Prod', color: 'red' as const, inherited: false, non_production: false }
const STAGING_TAG = { id: 2, name: 'Staging', color: 'amber' as const, inherited: false, non_production: false }

function container(partial: Partial<ConfigContainerDTO> & Pick<ConfigContainerDTO, 'name' | 'image' | 'tag'>): ConfigContainerDTO {
  return { base_image: '', log_group: '', ports: [], env_names: [], secret_names: [], ...partial }
}

const PAYMENTS: ConfigSearchItemDTO = {
  id: 'svc-payments',
  name: 'payments-api',
  cluster: { id: 'cl-core', name: 'core-services' },
  account_id: '482109375512',
  account_name: 'homelab-admin',
  region: 'us-east-1',
  tag: PROD_TAG,
  tag_inherited: true,
  family: 'payments-api',
  revision: 42,
  matched_revision: 42,
  previous: false,
  task_role: 'arn:aws:iam::482109375512:role/payments-api-task',
  execution_role: 'arn:aws:iam::482109375512:role/ecs-task-execution',
  containers: [
    container({
      name: 'payments-api',
      image: '482109375512.dkr.ecr.us-east-1.amazonaws.com/payments-api:1.18.2',
      tag: '1.18.2',
      base_image: 'node:18-alpine',
      log_group: '/ecs/core-services/payments-api',
      ports: [8080],
      env_names: ['NODE_ENV', 'PAYMENTS_PROVIDER', 'DB_POOL_SIZE'],
    }),
    container({ name: 'otel-collector', image: 'public.ecr.aws/aws-observability/aws-otel-collector:v0.40.0', tag: 'v0.40.0' }),
  ],
  matches: [
    { field: 'env', container: 'payments-api', value: 'NODE_ENV' },
    { field: 'base', container: 'payments-api', value: 'node:18-alpine' },
  ],
}

const NOTIFICATIONS: ConfigSearchItemDTO = {
  id: 'svc-notifications',
  name: 'notifications',
  cluster: { id: 'cl-core', name: 'core-services' },
  account_id: '482109375512',
  account_name: 'homelab-admin',
  region: 'us-east-1',
  tag: PROD_TAG,
  tag_inherited: true,
  family: 'notifications',
  revision: 10,
  matched_revision: 9,
  previous: true,
  task_role: 'arn:aws:iam::482109375512:role/notifications-task',
  execution_role: 'arn:aws:iam::482109375512:role/ecs-task-execution',
  containers: [
    container({
      name: 'notifications',
      image: '482109375512.dkr.ecr.us-east-1.amazonaws.com/notifications:2.2.0',
      tag: '2.2.0',
      log_group: '/ecs/core-services/notifications',
      ports: [8081],
      env_names: ['QUEUE_URL'],
    }),
  ],
  matches: [{ field: 'tag', container: 'notifications', value: '2.2.0' }],
}

const API_STAGING: ConfigSearchItemDTO = {
  id: 'svc-api',
  name: 'api',
  cluster: { id: 'cl-staging', name: 'staging-web' },
  account_id: '731022849906',
  account_name: 'staging-example',
  region: 'us-east-1',
  tag: STAGING_TAG,
  tag_inherited: true,
  family: 'api',
  revision: 21,
  matched_revision: 21,
  previous: false,
  task_role: 'arn:aws:iam::731022849906:role/api-task',
  execution_role: '=cmd|role',
  containers: [
    container({
      name: 'api',
      image: '731022849906.dkr.ecr.us-east-1.amazonaws.com/api:1.19.0-rc.2',
      tag: '1.19.0-rc.2',
      log_group: '/ecs/staging-web/api',
      ports: [8080, 9090],
      env_names: ['NODE_ENV', '=cmd'],
      secret_names: ['DATABASE_URL'],
    }),
  ],
  matches: [
    { field: 'env', container: 'api', value: 'NODE_ENV' },
    { field: 'secret', container: 'api', value: 'DATABASE_URL' },
  ],
}

function response(items: ConfigSearchItemDTO[], extra: Partial<ConfigSearchResponse> = {}): ConfigSearchResponse {
  return {
    query: 'env:NODE_ENV',
    items,
    total: items.length,
    truncated: false,
    searched: 15,
    unindexed: 0,
    polled_at: FIXTURE_NOW,
    ...extra,
  }
}

export const CONFIG_SEARCH_NODE: ConfigSearchResponse = response([API_STAGING, NOTIFICATIONS, PAYMENTS])
export const CONFIG_SEARCH_EMPTY: ConfigSearchResponse = response([], { query: 'nomatch' })
export const CONFIG_SEARCH_TRUNCATED: ConfigSearchResponse = response([PAYMENTS], { total: 2400, truncated: true })
export const CONFIG_SEARCH_UNINDEXED: ConfigSearchResponse = response([PAYMENTS], { unindexed: 2 })

export const configSearchHandlers = [
  http.get('/api/config-search', ({ request }) => {
    const q = new URL(request.url).searchParams.get('q') ?? ''
    if (q.includes('port:http')) {
      return HttpResponse.json({ error: { code: 'invalid', message: 'port: needs a number from 1 to 65535.' } }, { status: 422 })
    }
    if (q.includes('nomatch')) return HttpResponse.json(CONFIG_SEARCH_EMPTY)
    return HttpResponse.json({ ...CONFIG_SEARCH_NODE, query: q })
  }),
]
