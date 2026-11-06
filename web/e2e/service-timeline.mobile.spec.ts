import { SVC, serviceUrl } from './fixtures/ids'
import { expectPhoneLayoutClean, watchPage } from './fixtures/phone'
import { asUser, expect, test } from './fixtures/test'

// "Deployments, 14 days" on the phone: events that land on the right edge (a restart just now) used to centre their label on the edge, so the
// label stuck out of the panel and made <main> scroll sideways. The state is built here, not left to earlier specs: the service response is
// read from the real API and extended with the records a restart and failed deploys leave behind.
test.use({ ...asUser('sam'), viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true, deviceScaleFactor: 3 })

type DeploymentRecord = { deployment_id: string; revision: number | string; kind: string; outcome: string; started_at: string; finished_at: string | null }

async function withDeployments(page: import('@playwright/test').Page, records: (now: number) => DeploymentRecord[]) {
  await page.route(/\/api\/services\/[^/?]+(\?.*)?$/, async (route) => {
    const response = await route.fetch()
    const body = await response.json()
    body.deployments_14d = records(Date.now())
    await route.fulfill({ response, json: body })
  })
}

const record = (id: string, revision: number | string, kind: string, outcome: string, startedAt: number): DeploymentRecord => ({
  deployment_id: id,
  revision,
  kind,
  outcome,
  started_at: new Date(startedAt).toISOString(),
  finished_at: null,
})

const MINUTE = 60_000
const DAY = 86_400_000

for (const edge of ['right', 'left'] as const) {
  test(`an event at the ${edge} edge of "Deployments, 14 days" keeps its label inside the screen`, async ({ page }) => {
    watchPage(page)
    await withDeployments(page, (now) => [
      record('d1', 7, 'deploy', 'failed', edge === 'right' ? now - 2 * MINUTE : now - 14 * DAY + 2 * MINUTE),
      record('d2', 7, 'restart', 'succeeded', edge === 'right' ? now - MINUTE : now - 14 * DAY + MINUTE),
    ])
    await page.goto(serviceUrl(...SVC.paymentsApi, 'overview'))
    await expect(page.getByRole('heading', { name: 'Deployments, 14 days' })).toBeVisible()
    await page.waitForLoadState('networkidle')
    await expectPhoneLayoutClean(page)
  })
}

test('a long unbreakable event label cannot widen the page', async ({ page }) => {
  watchPage(page)
  await withDeployments(page, (now) => [record('d1', 'r'.repeat(60), 'deploy', 'failed', now - MINUTE)])
  await page.goto(serviceUrl(...SVC.paymentsApi, 'overview'))
  await expect(page.getByRole('heading', { name: 'Deployments, 14 days' })).toBeVisible()
  await page.waitForLoadState('networkidle')
  await expectPhoneLayoutClean(page)
})
