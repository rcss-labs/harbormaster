import type { APIRequestContext } from '@playwright/test'
import { SVC, clusterUrl, serviceUrl } from './fixtures/ids'
import { asUser, expect, test } from './fixtures/test'

// With a shared store (HM_KV_URL, as `make e2e` runs when it is exported) the cluster, service and list pages are
// served from versioned read models. The pages look exactly the same; harbormaster_snapshot_cache_requests_total says
// where they came from. Without a store this spec has nothing to observe and skips.
test.use(asUser('sam'))

const METRICS_TOKEN = 'e2e-metrics-token'

async function cacheRequests(request: APIRequestContext, model: string, result: string): Promise<number> {
  const response = await request.get('/metrics', { headers: { Authorization: `Bearer ${METRICS_TOKEN}` } })
  expect(response.status()).toBe(200)
  for (const line of (await response.text()).split('\n')) {
    if (line.startsWith(`harbormaster_snapshot_cache_requests_total{model="${model}",result="${result}"} `)) {
      return Number(line.slice(line.lastIndexOf(' ') + 1))
    }
  }
  return 0
}

test('the cluster, service and list pages are read through the snapshot cache', async ({ page, request }) => {
  test.skip(!process.env.HM_KV_URL, 'needs a shared store: export HM_KV_URL (make test-kv) before make e2e')

  // The poller marks every cluster after its first cycle; give it a moment, then load each page twice so the second
  // load can be a hit.
  await expect.poll(async () => {
    await page.goto(clusterUrl('482109375512', 'us-east-1', 'ci-tools'))
    await expect(page.getByRole('heading', { name: 'ci-tools', level: 1 })).toBeVisible()
    return (await cacheRequests(request, 'cluster', 'hit')) + (await cacheRequests(request, 'cluster', 'miss'))
  }, { timeout: 20_000 }).toBeGreaterThan(0)
  await page.reload()
  await expect(page.getByRole('heading', { name: 'ci-tools', level: 1 })).toBeVisible()
  await expect(page.getByText('jenkins-controller').first()).toBeVisible()
  expect(await cacheRequests(request, 'cluster', 'hit')).toBeGreaterThan(0)

  await page.goto(serviceUrl(...SVC.paymentsApi, 'overview'))
  await expect(page.getByRole('heading', { name: 'payments-api', level: 1 })).toBeVisible()
  await page.reload()
  await expect(page.getByRole('heading', { name: 'payments-api', level: 1 })).toBeVisible()
  expect(await cacheRequests(request, 'service', 'hit')).toBeGreaterThan(0)

  await page.goto('/clusters')
  await expect(page.getByText('core-services').first()).toBeVisible()
  await page.reload()
  await expect(page.getByText('core-services').first()).toBeVisible()
  expect(await cacheRequests(request, 'slice', 'hit')).toBeGreaterThan(0)
})
