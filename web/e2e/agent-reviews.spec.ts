import type { APIRequestContext, Page } from '@playwright/test'
import { expectPhoneLayoutClean, watchPage } from './fixtures/phone'
import { asUser, expect, test } from './fixtures/test'
import { createRoute, deleteE2eRoutes, KINDS, received, webhookBody } from './notifications.helpers'
import { saveScreenshot } from './helpers'

// Scheduled AI reviews end to end: an admin makes a review in settings, runs it now, the coordinator has the agent
// service (its scripted fake model reads the cost analytics) make the result as the agent identity, and the result
// reaches the in-process receiver and the app. The seed gives the app a weekly cost review and one finished result.
test.describe.configure({ mode: 'serial' })

const HOOK_PATH = '/fake-notify/webhook/e2e-reviews'
const SEEDED_REVIEW = 'Weekly cost'
const REVIEW_NAME = 'e2e Weekly spend'
const SUMMARY = 'This is a canned review from the demo model. It did not judge the services.'
const CAPTION = 'Written by AI from what its identity may read. Check it before acting.'

const csrf = async (request: APIRequestContext) => ({ 'X-CSRF-Token': ((await (await request.get('/api/me')).json()) as { csrf_token: string }).csrf_token })

interface RunRow {
  id: number
  name: string
  state: string
}

async function adminRuns(request: APIRequestContext): Promise<RunRow[]> {
  const response = await request.get('/api/agent/reviews/runs?limit=50')
  expect(response.ok(), await response.text()).toBe(true)
  return ((await response.json()) as { items: RunRow[] }).items
}

/** The id of the finished result the seed made, whichever other runs the specs added. */
async function seededRunId(request: APIRequestContext): Promise<number> {
  const seeded = (await adminRuns(request)).filter((run) => run.name === SEEDED_REVIEW && run.state === 'done').at(-1)
  expect(seeded, 'the seeded result').toBeDefined()
  return seeded!.id
}

async function deleteE2eReviews(request: APIRequestContext) {
  const list = (await (await request.get('/api/agent/reviews')).json()) as { items: { id: number; name: string }[] }
  for (const review of list.items.filter((item) => item.name.startsWith('e2e '))) {
    const response = await request.delete(`/api/agent/reviews/${review.id}`, { headers: await csrf(request) })
    expect(response.ok(), await response.text()).toBe(true)
  }
}

async function reviewIdOf(request: APIRequestContext, name: string): Promise<number> {
  const list = (await (await request.get('/api/agent/reviews')).json()) as { items: { id: number; name: string }[] }
  const found = list.items.find((item) => item.name === name)
  expect(found, `review ${name}`).toBeDefined()
  return found!.id
}

async function openEditor(page: Page) {
  await page.goto('/settings/agent')
  const panel = page.getByRole('region', { name: 'Reviews', exact: true })
  await expect(panel).toBeVisible()
  await panel.getByRole('button', { name: 'Add review' }).click()
  return panel
}

test.describe('sam', () => {
  test.use(asUser('sam'))
  let runId = 0

  test.beforeAll(async ({ request, baseURL }) => {
    // A route of its own that takes no alert, so the receiver at its path holds review messages only.
    await createRoute(request, webhookBody('e2e review sink', `${baseURL}${HOOK_PATH}`, { kinds: ['account_unreachable'], min_severity: 'critical' }))
  })
  test.afterAll(async ({ request }) => {
    await deleteE2eReviews(request)
    await deleteE2eRoutes(request)
  })

  test('an admin creates a weekly review and sees its next run', async ({ page, request }) => {
    await openEditor(page)
    await page.getByLabel('Review name').fill(REVIEW_NAME)
    await expect(page.getByRole('radio', { name: 'Cost' })).toBeChecked()
    await page.getByLabel('Cadence').selectOption('weekly')
    await page.getByLabel('Time', { exact: true }).fill('08:00')
    await page.getByLabel('Weekday').selectOption({ label: 'Monday' })
    await page.getByRole('group', { name: 'Routes' }).getByLabel('e2e review sink').check()
    await page.getByLabel('Review is on').check() // a new review starts off
    await expect(page.getByText(/^Next run: /)).toBeVisible()
    await page.getByRole('button', { name: 'Save review' }).click()
    await expect(page.getByText(`Saved "${REVIEW_NAME}".`)).toBeVisible()

    const row = page.getByRole('row').filter({ hasText: REVIEW_NAME })
    await expect(row.getByText(/^Weekly on Monday at 08:00/)).toBeVisible()
    await expect(row.getByText('No results yet')).toBeVisible()
    await expect(row.getByText('Not scheduled')).toHaveCount(0)
    const list = (await (await request.get('/api/agent/reviews')).json()) as { items: { name: string; enabled: boolean; next_run_at: string | null }[] }
    const stored = list.items.find((item) => item.name === REVIEW_NAME)!
    expect(stored.enabled).toBe(true)
    expect(Date.parse(stored.next_run_at ?? '')).toBeGreaterThan(Date.now())
  })

  test('run now produces a result and a review message at the receiver', async ({ page, request }) => {
    test.setTimeout(120_000)
    await page.goto('/settings/agent')
    await page.getByRole('button', { name: `Run now ${REVIEW_NAME}` }).click()
    await page.getByRole('dialog', { name: 'Run this review now?' }).getByRole('button', { name: 'Run review' }).click()
    await expect(page.getByText('The review has started. Its result goes to its routes.')).toBeVisible()

    const reviewId = await reviewIdOf(request, REVIEW_NAME)
    await expect
      .poll(async () => (await adminRuns(request)).find((run) => run.name === REVIEW_NAME)?.state, { timeout: 90_000, message: 'the run is done' })
      .toBe('done')
    runId = (await adminRuns(request)).find((run) => run.name === REVIEW_NAME)!.id
    expect(reviewId).toBeGreaterThan(0)

    const at = async () => (await received(request)).filter((entry) => entry.path === HOOK_PATH && entry.body.event === 'review')
    await expect.poll(async () => (await at()).length, { timeout: 30_000, message: 'the review message' }).toBe(1)
    const [message] = await at()
    expect(message.body).toMatchObject({ event: 'review', review_name: REVIEW_NAME, review_type: 'cost', review_run_id: runId, ai_generated: true })
    expect((await received(request)).filter((entry) => entry.path === HOOK_PATH && entry.body.event === 'review')).toHaveLength(1)
  })

  test('the result page shows the summary and its evidence', async ({ page }) => {
    await page.goto(`/reviews/runs/${runId}`)
    const about = page.getByRole('region', { name: 'About' })
    await expect(about.getByText(REVIEW_NAME)).toBeVisible()
    await expect(about.getByText('Cost', { exact: true })).toBeVisible()
    await expect(about.getByText('Review ready')).toBeVisible()
    const summary = page.getByRole('region', { name: 'Summary' })
    await expect(summary.getByText(SUMMARY)).toBeVisible()
    await expect(summary.getByText(CAPTION)).toBeVisible()
    const evidence = page.getByRole('region', { name: 'Evidence' })
    await expect(evidence.getByText('The first thing the demo model read.')).toBeVisible()
    await saveScreenshot(page, 'agent-review-result.png')
  })

  test('the seeded result is on the reviews page after a reload', async ({ page, request }) => {
    const seededId = await seededRunId(request)
    await page.goto('/reviews')
    const row = page.locator('[data-review-row]').filter({ hasText: SEEDED_REVIEW })
    await expect(row.getByRole('link', { name: SEEDED_REVIEW })).toHaveAttribute('href', `/reviews/runs/${seededId}`)
    await expect(row.getByText('Review ready')).toBeVisible()
    await page.reload()
    await expect(page.locator('[data-review-row]').filter({ hasText: SEEDED_REVIEW }).getByText('Review ready')).toBeVisible()
    await saveScreenshot(page, 'agent-reviews-list.png')
  })

  test('a PagerDuty route is not offered in the editor', async ({ page, request }) => {
    await createRoute(request, webhookBody('e2e pagerduty', '', { destination: 'pagerduty', pagerduty_region: 'us', routing_key: 'demo00000000000000000000000000ab', kinds: KINDS, min_severity: 'critical' }))
    await openEditor(page)
    const routes = page.getByRole('group', { name: 'Routes' })
    await expect(routes.getByLabel('e2e review sink')).toBeVisible()
    await expect(routes.getByLabel('e2e pagerduty')).toHaveCount(0)
    await expect(routes.getByText(/PagerDuty routes are not offered/)).toBeVisible()
  })
})

test.describe('omer', () => {
  test.use(asUser('omer'))

  // omer has no access to Prod, a tag every result covers, so he reads none of them.
  test('a viewer without a covered tag does not find the result in the list or by its address', async ({ page, browser }) => {
    const admin = await browser.newContext({ storageState: 'e2e/.auth/sam.json' })
    let seededId: number
    try {
      seededId = await seededRunId(admin.request)
    } finally {
      await admin.close()
    }
    const listed = (await (await page.request.get('/api/reviews/runs')).json()) as { items: { name: string }[] }
    expect(listed.items).toEqual([])
    expect((await page.request.get(`/api/reviews/runs/${seededId}`)).status()).toBe(404)

    await page.goto('/reviews')
    await expect(page.getByText('No review results yet.')).toBeVisible()
    await expect(page.getByText(SEEDED_REVIEW)).toHaveCount(0)
    await page.goto(`/reviews/runs/${seededId}`)
    await expect(page.getByText('Page not found.')).toBeVisible()
    await expect(page.getByText(SUMMARY)).toHaveCount(0)
  })
})

test.describe('sam on a phone', () => {
  test.use({ ...asUser('sam'), viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true, deviceScaleFactor: 3 })

  test('the reviews page and a result fit a phone', async ({ page, request }) => {
    watchPage(page)
    const seededId = await seededRunId(request)
    await page.goto('/reviews')
    await expect(page.locator('[data-review-row]').filter({ hasText: SEEDED_REVIEW }).getByText('Review ready')).toBeVisible()
    await expectPhoneLayoutClean(page)
    await page.goto(`/reviews/runs/${seededId}`)
    await expect(page.getByRole('region', { name: 'Summary' }).getByText(SUMMARY)).toBeVisible()
    await expect(page.getByRole('region', { name: 'Evidence' })).toBeVisible()
    await expectPhoneLayoutClean(page)
    await saveScreenshot(page, 'agent-review-result-phone.png')
  })
})
