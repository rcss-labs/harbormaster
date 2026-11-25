import type { APIRequestContext, Page } from '@playwright/test'
import { expectPhoneLayoutClean, watchPage } from './fixtures/phone'
import { asUser, expect, test } from './fixtures/test'
import { createRoute, deleteE2eRoutes, received, webhookBody } from './notifications.helpers'
import { saveScreenshot } from './helpers'

// Admin-written AI reviews end to end: an admin writes the instructions in settings, the coordinator has the agent
// service (its scripted fake model) make the result from a copy of them, and the result reaches the in-process receiver
// and the app. A test run is stored and sends nothing. The seed gives the app a disabled "Idle services" review and one
// finished result of it with its copy.
test.describe.configure({ mode: 'serial' })

const HOOK_PATH = '/fake-notify/webhook/e2e-custom-reviews'
const SEEDED_REVIEW = 'Idle services'
const REVIEW_NAME = 'e2e Idle custom'
const TEXT = 'Look at the quiet zebra-mango services.\nName the five that cost the most while idle.'
const EDITED_TEXT = 'Look only at the loud kumquat-falcon services.\nKeep it to two sentences.'
const SUMMARY = 'This is a canned review from the demo model. It did not judge the services.'
// Assembled from two pieces so no secret scanner takes the file for a leak.
const PASTED_TOKEN = `ghp_${'a1B2c3D4e5F6g7H8i9J0k1L2m3N4o5P6q7R8'}`

const csrf = async (request: APIRequestContext) => ({ 'X-CSRF-Token': ((await (await request.get('/api/me')).json()) as { csrf_token: string }).csrf_token })

interface RunRow {
  id: number
  name: string
  state: string
  test: boolean
  instructions: string
}

async function adminRuns(request: APIRequestContext): Promise<RunRow[]> {
  const response = await request.get('/api/agent/reviews/runs?limit=50')
  expect(response.ok(), await response.text()).toBe(true)
  return ((await response.json()) as { items: RunRow[] }).items
}

interface ReviewRow {
  id: number
  name: string
  type: string
  enabled: boolean
  instructions: string
  version: number
}

async function reviews(request: APIRequestContext): Promise<ReviewRow[]> {
  return ((await (await request.get('/api/agent/reviews')).json()) as { items: ReviewRow[] }).items
}

async function deleteE2eReviews(request: APIRequestContext) {
  for (const review of (await reviews(request)).filter((item) => item.name.startsWith('e2e '))) {
    const response = await request.delete(`/api/agent/reviews/${review.id}`, { headers: await csrf(request) })
    expect(response.ok(), await response.text()).toBe(true)
  }
}

/** The finished, delivered run of the e2e review, whichever other runs exist. */
async function deliveredRun(request: APIRequestContext): Promise<RunRow> {
  const run = (await adminRuns(request)).find((item) => item.name === REVIEW_NAME && item.state === 'done' && !item.test)
  expect(run, 'the delivered run').toBeDefined()
  return run!
}

async function openEditor(page: Page) {
  await page.goto('/settings/agent')
  const panel = page.getByRole('region', { name: 'Reviews', exact: true })
  await expect(panel).toBeVisible()
  await panel.getByRole('button', { name: 'Add review' }).click()
  return panel
}

async function fillCustomReview(page: Page, name: string, text: string) {
  await page.getByLabel('Review name').fill(name)
  await page.getByRole('radio', { name: 'Custom' }).check()
  await page.getByLabel('Instructions', { exact: true }).fill(text)
  await page.getByRole('group', { name: 'Routes' }).getByLabel('e2e custom sink').check()
}

async function openReviewEditor(page: Page, name: string) {
  await page.goto('/settings/agent')
  await page.getByRole('button', { name: `Edit ${name}` }).click()
}

test.describe('sam', () => {
  test.use(asUser('sam'))
  let testRunId = 0

  test.beforeAll(async ({ request, baseURL }) => {
    // A route of its own that takes no alert, so the receiver at its path holds review messages only.
    await createRoute(request, webhookBody('e2e custom sink', `${baseURL}${HOOK_PATH}`, { kinds: ['account_unreachable'], min_severity: 'critical' }))
  })
  test.afterAll(async ({ request }) => {
    await deleteE2eReviews(request)
    await deleteE2eRoutes(request)
  })

  test('an admin writes a custom review and sees it listed', async ({ page, request }) => {
    await openEditor(page)
    await page.getByLabel('Review name').fill(REVIEW_NAME)
    await page.getByRole('radio', { name: 'Custom' }).check()
    // The field asks for its text at once, before anything is typed.
    await expect(page.getByText('Write the instructions.')).toBeVisible()
    await page.getByLabel('Instructions', { exact: true }).fill(TEXT)
    await expect(page.getByText('Write the instructions.')).toHaveCount(0)
    await page.getByRole('group', { name: 'Routes' }).getByLabel('e2e custom sink').check()
    await page.getByRole('button', { name: 'Save review' }).click()
    await expect(page.getByText(`Saved "${REVIEW_NAME}".`)).toBeVisible()

    const row = page.getByRole('row').filter({ hasText: REVIEW_NAME })
    await expect(row.getByText('No results yet')).toBeVisible()
    const stored = (await reviews(request)).find((item) => item.name === REVIEW_NAME)!
    expect(stored).toMatchObject({ type: 'custom', enabled: false, instructions: TEXT })

    // The seed's review is there too: off, with a result.
    const seeded = page.getByRole('row').filter({ hasText: SEEDED_REVIEW })
    await expect(seeded.getByRole('switch', { name: `${SEEDED_REVIEW} is on` })).not.toBeChecked()
    await expect(seeded.getByRole('link', { name: /^Done/ })).toBeVisible()
    await saveScreenshot(page, 'agent-custom-review-list.png')
  })

  test('a pasted token is refused and nothing is saved', async ({ page, request }) => {
    await openEditor(page)
    await fillCustomReview(page, 'e2e Token review', `Check these services.\nToken ${PASTED_TOKEN}`)
    await page.getByRole('button', { name: 'Save review' }).click()
    await expect(page.getByRole('alert').filter({ hasText: 'looks like a secret' })).toBeVisible()
    await expect(page.getByText('Saved "e2e Token review".')).toHaveCount(0)
    expect((await reviews(request)).some((item) => item.name === 'e2e Token review')).toBe(false)
    // The refused text is nowhere the admin can read it back.
    const stored = JSON.stringify(await reviews(request)) + JSON.stringify(await adminRuns(request))
    expect(stored).not.toContain(PASTED_TOKEN)
  })

  test('a test run produces a result and sends nothing to the receiver', async ({ page, request }) => {
    test.setTimeout(120_000)
    await page.goto('/settings/agent')
    await page.getByRole('button', { name: `Test run ${REVIEW_NAME}` }).click()
    await page.getByRole('dialog', { name: 'Test run this review?' }).getByRole('button', { name: 'Start test run' }).click()
    await expect(page.getByText('The test run has started. Its result appears under Past results.')).toBeVisible()

    await expect
      .poll(async () => (await adminRuns(request)).find((run) => run.name === REVIEW_NAME && run.test)?.state, { timeout: 90_000, message: 'the test run is done' })
      .toBe('done')
    testRunId = (await adminRuns(request)).find((run) => run.name === REVIEW_NAME && run.test)!.id

    await page.goto(`/reviews/runs/${testRunId}`)
    await expect(page.getByText('Test run. It was not sent.')).toBeVisible()
    await expect(page.getByRole('region', { name: 'Summary' }).getByText(SUMMARY)).toBeVisible()
    // The dispatcher plans a done run at its next cycle (2 s here), and a test run is never planned. The next test
    // delivers a run, which proves a cycle passed after this one was done, and checks the receiver again.
    expect((await received(request)).filter((entry) => entry.path === HOOK_PATH)).toEqual([])
  })

  test('run now sends a message headed Custom review', async ({ page, request }) => {
    test.setTimeout(120_000)
    await page.goto('/settings/agent')
    await page.getByRole('button', { name: `Run now ${REVIEW_NAME}` }).click()
    await page.getByRole('dialog', { name: 'Run this review now?' }).getByRole('button', { name: 'Run review' }).click()
    await expect(page.getByText('The review has started. Its result goes to its routes.')).toBeVisible()

    await expect
      .poll(async () => (await adminRuns(request)).find((run) => run.name === REVIEW_NAME && !run.test)?.state, { timeout: 90_000, message: 'the run is done' })
      .toBe('done')
    const run = await deliveredRun(request)
    const at = async () => (await received(request)).filter((entry) => entry.path === HOOK_PATH)
    await expect.poll(async () => (await at()).length, { timeout: 30_000, message: 'the review message' }).toBeGreaterThan(0)
    const messages = await at()
    // Exactly one message, for the run made by "Run now": the test run, done earlier, was passed by.
    expect(messages).toHaveLength(1)
    expect(messages[0].body).toMatchObject({ event: 'review', title: `Custom review: ${REVIEW_NAME}`, review_name: REVIEW_NAME, review_type: 'custom', review_run_id: run.id, ai_generated: true })
    expect(JSON.stringify(messages[0].body)).not.toContain('zebra-mango')
    expect(run.id).not.toBe(testRunId)
  })

  test('past results show the instructions a run used', async ({ page, request }) => {
    // The text changes after the runs; each run keeps the one it used.
    await openReviewEditor(page, REVIEW_NAME)
    await page.getByLabel('Instructions', { exact: true }).fill(EDITED_TEXT)
    await page.getByRole('button', { name: 'Save review' }).click()
    await expect(page.getByText(`Saved "${REVIEW_NAME}".`)).toBeVisible()

    const past = page.getByRole('region', { name: `Past results of ${REVIEW_NAME}` })
    await expect(past.getByText('Instructions used')).toHaveCount(2)
    await expect(past.getByText('Test', { exact: true })).toHaveCount(1)
    for (const summary of await past.getByText('Instructions used').all()) await summary.click()
    const used = past.locator('pre')
    await expect(used).toHaveCount(2)
    for (const text of await used.allTextContents()) expect(text).toBe(TEXT)
    expect((await reviews(request)).find((item) => item.name === REVIEW_NAME)!.instructions).toBe(EDITED_TEXT)
    await saveScreenshot(page, 'agent-custom-review-past-results.png')

    // The seed's result keeps its copy as well, and is no test run.
    const seeded = (await adminRuns(request)).find((run) => run.name === SEEDED_REVIEW)!
    expect(seeded).toMatchObject({ state: 'done', test: false })
    await openReviewEditor(page, SEEDED_REVIEW)
    const seededPast = page.getByRole('region', { name: `Past results of ${SEEDED_REVIEW}` })
    await seededPast.getByText('Instructions used').click()
    await expect(seededPast.locator('pre')).toHaveText(seeded.instructions)
    expect(seeded.instructions.split('\n')).toHaveLength(3)
  })

  test('the type of a saved review cannot become Custom', async ({ page, request }) => {
    // A saved built-in review is not offered Custom, and a saved custom one shows only Custom, locked.
    await openReviewEditor(page, 'Weekly cost')
    await expect(page.getByRole('radio', { name: 'Cost' })).toBeChecked()
    await expect(page.getByRole('radio', { name: 'Custom' })).toHaveCount(0)
    await expect(page.getByLabel('Instructions', { exact: true })).toHaveCount(0)
    await openReviewEditor(page, REVIEW_NAME)
    await expect(page.getByRole('radio')).toHaveCount(1)
    await expect(page.getByRole('radio', { name: 'Custom' })).toBeDisabled()

    // Asked for directly, the server refuses in both directions and changes nothing.
    const [weekly] = (await reviews(request)).filter((item) => item.name === 'Weekly cost')
    const detail = ((await (await request.get('/api/agent/reviews')).json()) as { items: Record<string, unknown>[] }).items.find((item) => item.id === weekly.id)!
    const toCustom = await request.put(`/api/agent/reviews/${weekly.id}`, {
      headers: await csrf(request),
      data: { name: detail.name, type: 'custom', enabled: detail.enabled, schedule: detail.schedule, scope: [], route_ids: detail.route_ids, instructions: TEXT, version: weekly.version },
    })
    expect(toCustom.status()).toBe(422)
    expect(await toCustom.json()).toMatchObject({ error: { code: 'type_fixed', field: 'type' } })
    const custom = (await reviews(request)).find((item) => item.name === REVIEW_NAME)!
    const customDetail = ((await (await request.get('/api/agent/reviews')).json()) as { items: Record<string, unknown>[] }).items.find((item) => item.id === custom.id)!
    const fromCustom = await request.put(`/api/agent/reviews/${custom.id}`, {
      headers: await csrf(request),
      data: { name: customDetail.name, type: 'cost', enabled: customDetail.enabled, schedule: customDetail.schedule, scope: [], route_ids: customDetail.route_ids, version: custom.version },
    })
    expect(fromCustom.status()).toBe(422)
    expect(await fromCustom.json()).toMatchObject({ error: { code: 'type_fixed', field: 'type' } })
    const after = await reviews(request)
    expect(after.find((item) => item.id === weekly.id)).toMatchObject({ type: 'cost', version: weekly.version })
    expect(after.find((item) => item.id === custom.id)).toMatchObject({ type: 'custom', instructions: EDITED_TEXT })
  })
})

// dana may View every tag the agent identity may, so she reads delivered results, but a test run and the instructions
// are for admins.
test.describe('dana', () => {
  test.use(asUser('dana'))

  test('a viewer reads the delivered result but finds neither the test run nor the instructions', async ({ page, browser }) => {
    const admin = await browser.newContext({ storageState: 'e2e/.auth/sam.json' })
    let run: RunRow
    let testRun: RunRow
    try {
      run = await deliveredRun(admin.request)
      testRun = (await adminRuns(admin.request)).find((item) => item.name === REVIEW_NAME && item.test)!
    } finally {
      await admin.close()
    }
    const bodies: string[] = []
    page.on('response', (response) => {
      if (response.url().includes('/api/')) void response.text().then((text) => bodies.push(text)).catch(() => {})
    })
    await page.goto(`/reviews/runs/${run.id}`)
    await expect(page.getByRole('region', { name: 'About' }).getByText(REVIEW_NAME)).toBeVisible()
    await expect(page.getByRole('region', { name: 'Summary' }).getByText(SUMMARY)).toBeVisible()
    await page.goto('/reviews')
    await expect(page.locator('[data-review-row]').filter({ hasText: REVIEW_NAME })).toHaveCount(1)
    await expect(page.getByText('Test run')).toHaveCount(0)

    expect(bodies.length).toBeGreaterThan(0)
    for (const body of bodies) {
      expect(body).not.toContain('zebra-mango')
      expect(body).not.toContain('kumquat-falcon')
      expect(body).not.toContain('instructions')
    }
    await expect(page.getByText('zebra-mango')).toHaveCount(0)

    // By its address, and in the list, the test run does not exist for her; the admin routes refuse her.
    const listed = (await (await page.request.get('/api/reviews/runs')).json()) as { items: { id: number }[] }
    expect(listed.items.map((item) => item.id)).not.toContain(testRun.id)
    expect((await page.request.get(`/api/reviews/runs/${testRun.id}`)).status()).toBe(404)
    await page.goto(`/reviews/runs/${testRun.id}`)
    await expect(page.getByText('Page not found.')).toBeVisible()
    expect((await page.request.get('/api/agent/reviews')).status()).toBe(403)
    expect((await page.request.get('/api/agent/reviews/runs')).status()).toBe(403)
  })
})

test.describe('sam on a phone', () => {
  test.use({ ...asUser('sam'), viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true, deviceScaleFactor: 3 })

  test('the instructions field and a custom result fit a phone', async ({ page, request }) => {
    watchPage(page)
    await page.goto('/settings/agent')
    await page.getByRole('button', { name: 'Add review' }).click()
    const sheet = page.getByRole('dialog', { name: 'Review editor' })
    await sheet.getByLabel('Review name').fill('e2e Phone custom')
    await sheet.getByRole('radio', { name: 'Custom' }).check()
    await sheet.getByLabel('Instructions', { exact: true }).fill(TEXT)
    await expect(sheet.getByLabel('Instructions', { exact: true })).toHaveValue(TEXT)
    // The settings page is not held to the phone layout rules as a whole (its selects and checkboxes are small, as in
    // phase 3), so this checks what is new: the text field sits inside the screen and nothing scrolls sideways.
    const field = sheet.getByLabel('Instructions', { exact: true })
    await field.scrollIntoViewIfNeeded()
    const box = (await field.boundingBox())!
    expect(box.x).toBeGreaterThanOrEqual(0)
    expect(box.x + box.width).toBeLessThanOrEqual(390)
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth)).toBe(true)
    expect(await sheet.evaluate((el) => el.scrollWidth <= el.clientWidth)).toBe(true)
    await saveScreenshot(page, 'agent-custom-review-editor-phone.png')

    const run = await deliveredRun(request)
    await page.goto(`/reviews/runs/${run.id}`)
    await expect(page.getByRole('region', { name: 'Summary' }).getByText(SUMMARY)).toBeVisible()
    await expect(page.getByRole('region', { name: 'About' }).getByText('Custom', { exact: true })).toBeVisible()
    await expectPhoneLayoutClean(page)
    await saveScreenshot(page, 'agent-custom-review-result-phone.png')
  })
})
