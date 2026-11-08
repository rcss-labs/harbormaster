import type { Page } from '@playwright/test'
import { SVC, serviceUrl } from './fixtures/ids'
import { asUser, expect, test } from './fixtures/test'

// Read-only: runs in the observe project (the `observe.spec.ts` pattern matches this name), so it sees the unmutated seed
// before change.spec rolls image-resizer back and gives it running tasks. Nothing here applies a deploy or a rollback.

const track = (page: Page) => page.getByTestId('timeline-track')

test.describe('as sam, a global admin', () => {
  test.use(asUser('sam'))

  test('a failing service shows its stops and alerts on the real API', async ({ page }) => {
    await page.goto(serviceUrl(...SVC.imageResizer, 'timeline'))
    const stops = track(page).getByRole('button', { name: /stopped$/ })
    await expect(stops.first()).toBeVisible()
    await expect(track(page).getByRole('button', { name: /Alert active/ }).first()).toBeVisible()

    await stops.first().click()
    const details = page.getByRole('region', { name: 'Details' })
    await expect(details.getByText('OutOfMemoryError').first()).toBeVisible()

    await details.getByRole('button', { name: 'Open task' }).click()
    await expect(page.getByRole('dialog')).toBeVisible()
  })

  test('log errors appear for a user with Logs', async ({ page }) => {
    await page.goto(serviceUrl(...SVC.paymentsApi, 'timeline'))
    const markers = track(page).getByRole('button', { name: /error lines? in / })
    // A cold scan is paced at 2 CloudWatch calls a second (about 6 s for the first view of a range), so allow for it.
    await expect(markers.first()).toBeVisible({ timeout: 20_000 })

    await markers.first().click()
    await page.getByRole('link', { name: 'Open logs at this time' }).click()
    await expect(page).toHaveURL(/\/logs\?before=/)
    await expect(page.getByText(/^Showing logs up to /)).toBeVisible()
  })

  test('rollback from the trigger opens the Deploy review', async ({ page }) => {
    // No seeded service has a change shortly before its first failure, so the incident is built here: the real
    // response is read and extended with a deploy of the live revision as the trigger, rolling back to the real previous one.
    const revisionsResponse = await page.request.get(`/api/services/${serviceUrl(...SVC.paymentsApi).split('/')[2]}/revisions`)
    const revisions = (await revisionsResponse.json()) as { deployed_revision: number; items: { revision: number; status: string }[] }
    const previous = revisions.items.find((item) => item.status === 'previous')
    expect(previous, 'payments-api has a previous revision to roll back to').toBeDefined()

    await page.route(/\/api\/services\/[^/]+\/timeline\?/, async (route) => {
      const response = await route.fetch()
      const body = await response.json()
      const now = Date.now()
      const trigger = {
        id: 'op:e2e-trigger',
        lane: 'change',
        severity: 'change',
        title: `Deployed revision ${revisions.deployed_revision}`,
        actor: 'sam',
        detail: '',
        op: 'deploy',
        task_id: '',
        at: new Date(now - 20 * 60_000).toISOString(),
        evidence_at: null,
        diff: [],
        from_revision: previous!.revision,
        to_revision: revisions.deployed_revision,
      }
      body.events = [...body.events, trigger]
      body.incident = {
        first_failure_at: new Date(now - 10 * 60_000).toISOString(),
        last_failure_at: new Date(now - 2 * 60_000).toISOString(),
        failure_count: 3,
        top_reason: '',
        top_reason_count: 0,
        trigger_unknown: false,
        trigger,
        rollback_revision: previous!.revision,
        rolled_back: null,
      }
      await route.fulfill({ response, json: body })
    })

    await page.goto(serviceUrl(...SVC.paymentsApi, 'timeline'))
    await expect(page.getByText(`Tasks began failing 10 minutes after revision ${revisions.deployed_revision}.`)).toBeVisible()
    await page.getByRole('button', { name: 'Show the change' }).click()
    await page.getByRole('button', { name: `Roll back to revision ${previous!.revision}` }).click()

    await expect(page).toHaveURL(/\/deploy\?/)
    const review = page.getByRole('dialog')
    await expect(review.getByRole('heading', { name: 'Review changes to payments-api' })).toBeVisible()
    // Stop at Review: closing it leaves the estate unchanged.
    await review.getByRole('button', { name: 'Keep editing' }).click()
    await expect(review).toBeHidden()
  })
})

test.describe('as omer, without Logs on the staging tag', () => {
  test.use(asUser('omer'))

  test('a viewer without Logs sees the lane locked', async ({ page }) => {
    const logErrorCalls: string[] = []
    page.on('request', (request) => {
      if (request.url().includes('log-errors')) logErrorCalls.push(request.url())
    })
    await page.goto(serviceUrl(...SVC.stagingApi, 'timeline'))
    await expect(page.getByRole('heading', { name: 'api', level: 1 })).toBeAttached()
    await expect(page.getByTestId('timeline-toggles').getByRole('checkbox', { name: 'Log errors' })).toBeDisabled()
    await expect(page.getByText(/You need Logs on /).first()).toBeVisible()
    await page.waitForLoadState('networkidle')
    expect(logErrorCalls).toEqual([])
  })
})
