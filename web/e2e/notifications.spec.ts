import {
  ROUTE_PREFIX,
  createRoute,
  deleteE2eRoutes,
  failingServices,
  openLog,
  openNotifications,
  pickScope,
  received,
  receivedAt,
  routeRow,
  startRoute,
  webhookBody,
} from './notifications.helpers'
import { asUser, expect, test } from './fixtures/test'

// Alert notifications end to end against the in-process fake receiver (/fake-notify/): no internet. The dispatcher
// cycles every 2s and the settle time is 0 (playwright.config.ts), so the seeded failures notify at once. Every route
// the specs make is named "e2e ..." and deleted afterwards, also on failure.
test.describe.configure({ mode: 'serial' })

test.describe('sam', () => {
  test.use(asUser('sam'))
  test.afterEach(async ({ request }) => deleteE2eRoutes(request))

  const hookUrl = (base: string | undefined, name: string) => `${base}/fake-notify/webhook/${name}`

  test('a route sends a test, then the open alerts, and its URL is never shown again', async ({ page, request, baseURL }) => {
    const since = new Date()
    await openNotifications(page)
    await startRoute(page, 'e2e hook', 'Webhook')
    await page.getByLabel('Webhook URL').fill(hookUrl(baseURL, 'ok'))
    // The preview names what the route would send right now, from the seeded failures.
    await expect(page.getByText(/Right now this route would send [1-9]\d* notifications?\./)).toBeVisible({ timeout: 15_000 })
    await page.getByRole('button', { name: 'Save route' }).click()
    await expect(page.getByText('Saved "e2e hook".')).toBeVisible()

    await page.getByRole('button', { name: 'Send test' }).click()
    await expect(page.getByText(/^Test sent to .*See the delivery log\.$/)).toBeVisible()
    await openLog(page)
    const testRow = page.getByRole('row').filter({ hasText: 'Test message' }).first()
    await expect(testRow).toContainText('Delivered')

    // Enable it: within 15 s the receiver holds a raised alert, and the log a delivered row for it.
    await page.getByRole('button', { name: 'Routes', exact: true }).click()
    await routeRow(page, 'e2e hook').click()
    await page.getByLabel('Route is on').check()
    await page.getByRole('button', { name: 'Save route' }).click()
    await expect(page.getByText('Saved "e2e hook".').last()).toBeVisible()
    await expect
      .poll(async () => (await receivedAt(request, '/fake-notify/webhook/ok', since)).some((entry) => entry.body?.event === 'alert.raised'), { timeout: 15_000 })
      .toBe(true)
    await openLog(page)
    await expect(page.getByRole('row').filter({ hasText: 'Delivered' }).filter({ hasNotText: 'Test message' }).first()).toBeVisible({ timeout: 15_000 })

    // The stored URL is write-only: after a reload the field is empty and the page does not contain it.
    await page.reload()
    await routeRow(page, 'e2e hook').click()
    await expect(page.getByLabel('Webhook URL')).toHaveValue('')
    await expect(page.getByLabel('Webhook URL')).toHaveAttribute('placeholder', 'Saved. Type to replace.')
    expect(await page.locator('body').innerText()).not.toContain('/fake-notify/webhook/ok')
  })

  test('a failing receiver shows Failed, the banner and a retry that stays failed', async ({ page, baseURL }) => {
    await openNotifications(page)
    await startRoute(page, 'e2e failing', 'Webhook')
    await page.getByLabel('Webhook URL').fill(hookUrl(baseURL, 'fail'))
    await page.getByRole('button', { name: 'Save route' }).click()
    await expect(page.getByText('Saved "e2e failing".')).toBeVisible()
    await page.getByRole('button', { name: 'Send test' }).click()
    await expect(page.getByText('HTTP 503')).toBeVisible()

    await page.getByRole('button', { name: 'Open delivery log' }).click()
    await expect(page).toHaveURL(/view=log&status=failed/)
    const failed = page.getByRole('row').filter({ hasText: 'Test message' }).first()
    await expect(failed).toContainText('Failed')
    await expect(failed).toContainText('HTTP 503')
    await failed.getByRole('button', { name: 'Retry' }).click()
    // The retry runs the same attempts again: while they last the row keeps the HTTP 503 note.
    await expect(page.getByRole('row').filter({ hasText: 'Test message' }).first()).toContainText('HTTP 503')
  })

  test('cluster threshold 1 and a service route send two notifications; threshold 2 sends only the service', async ({ page, request, baseURL }) => {
    // The seeded estate: a cluster with exactly one failing service and at least two services.
    const targets = (await (await request.get('/api/notifications/targets')).json()) as { services: { cluster: string }[] }
    const failing = await failingServices(request)
    const candidate = failing.find((entry) => failing.filter((other) => other.cluster === entry.cluster).length === 1 && targets.services.filter((s) => s.cluster === entry.cluster).length >= 2)
    expect(candidate, 'no seeded cluster has exactly one failing service and two services').toBeDefined()
    const { cluster, service, alerts } = candidate!
    const total = targets.services.filter((s) => s.cluster === cluster).length
    const since = new Date()

    await openNotifications(page)
    const make = async (name: string, path: string, scope: () => Promise<void>) => {
      await startRoute(page, name, 'Webhook')
      await page.getByLabel('Webhook URL').fill(hookUrl(baseURL, path))
      await scope()
      await page.getByLabel('Route is on').check()
    }
    const save = async (name: string) => {
      await page.getByRole('button', { name: 'Save route' }).click()
      await expect(page.getByText(`Saved "${name}".`)).toBeVisible()
    }

    await make('e2e cluster any', 'c1', async () => {
      await pickScope(page, cluster, new RegExp(`^${cluster}\\b`))
      await expect(page.getByText(/Right now this route would send 1 notification\./)).toBeVisible({ timeout: 15_000 })
    })
    await save('e2e cluster any')

    await make('e2e cluster two', 'c2', async () => {
      await pickScope(page, cluster, new RegExp(`^${cluster}\\b`))
      const needed = page.getByRole('spinbutton', { name: `Failing services needed for ${cluster}` })
      await needed.fill('2')
      await expect(page.getByText(`${cluster}: 1 of ${total} services failing, below 2. Nothing is sent.`)).toBeVisible({ timeout: 15_000 })
    })
    await save('e2e cluster two')

    await make('e2e service', 'svc', async () => {
      await pickScope(page, service, new RegExp(`^${service} · ${cluster}`))
      await expect(page.getByText(new RegExp(`Right now this route would send ${alerts} notifications?\\.`))).toBeVisible({ timeout: 15_000 })
    })
    await save('e2e service')

    await expect
      .poll(async () => (await receivedAt(request, '/fake-notify/webhook/c1', since)).length, { timeout: 15_000 })
      .toBe(1)
    await expect.poll(async () => (await receivedAt(request, '/fake-notify/webhook/svc', since)).length, { timeout: 15_000 }).toBe(alerts)
    await page.waitForTimeout(5_000) // c2 must stay silent beyond two dispatcher cycles

    const cluster1 = await receivedAt(request, '/fake-notify/webhook/c1', since)
    expect(cluster1[0].body).toMatchObject({ scope: 'cluster', failing: 1, total })
    expect(cluster1[0].body.services.map((s: { service: string }) => s.service)).toContain(service)
    const svc = await receivedAt(request, '/fake-notify/webhook/svc', since)
    for (const entry of svc) expect(entry.body).toMatchObject({ scope: 'service', service })
    expect(await receivedAt(request, '/fake-notify/webhook/c2', since)).toEqual([])

    // The delivery log shows the aggregated notification and links to the cluster.
    await openLog(page)
    const row = page.getByRole('row').filter({ hasText: `${cluster}: 1 of ${total} services failing` }).first()
    await expect(row).toBeVisible({ timeout: 15_000 })
    await expect(row.getByRole('link', { name: `${cluster}: 1 of ${total} services failing` })).toHaveAttribute('href', /^\/clusters\//)
  })

  test('a catch-all gets only what no other route notified', async ({ page, request, baseURL }) => {
    const failing = await failingServices(request)
    expect(failing.length, 'the seeded estate has failing services').toBeGreaterThanOrEqual(2)
    const covered = failing[0]
    const since = new Date()
    const pick = await (await request.get('/api/notifications/targets')).json()
    const service = (pick.services as { id: string; name: string; cluster: string }[]).find((s) => s.name === covered.service && s.cluster === covered.cluster)
    expect(service).toBeDefined()
    await createRoute(request, webhookBody('e2e covers one', hookUrl(baseURL, 'covers'), { scope: [{ kind: 'service', service_id: service!.id }] }))
    await createRoute(request, webhookBody('e2e rest', hookUrl(baseURL, 'rest'), { fallback: true }))

    const others = failing.filter((entry) => entry.service !== covered.service)
    await expect.poll(async () => (await receivedAt(request, '/fake-notify/webhook/rest', since)).length, { timeout: 15_000 }).toBeGreaterThan(0)
    await page.waitForTimeout(3_000)
    const rest = await receivedAt(request, '/fake-notify/webhook/rest', since)
    for (const entry of rest) expect(entry.body.service).not.toBe(covered.service)
    expect(rest.some((entry) => others.some((other) => other.service === entry.body.service))).toBe(true)
    expect((await receivedAt(request, '/fake-notify/webhook/covers', since)).every((entry) => entry.body.service === covered.service)).toBe(true)
  })

  test('Telegram: a route sends a test with the token only in the secret, and a refused chat fails clearly', async ({ page, request }) => {
    const token = '123456789:' + 'Ab-_'.repeat(9)
    const since = new Date()
    await openNotifications(page)
    await startRoute(page, 'e2e telegram', 'Telegram')
    await page.getByLabel('Bot token').fill(token)
    await page.getByLabel('Chat ID').fill('-1001')
    await page.getByRole('button', { name: 'Save route' }).click()
    await expect(page.getByText('Saved "e2e telegram".')).toBeVisible()
    await page.getByRole('button', { name: 'Send test' }).click()
    await expect(page.getByText(/^Test sent to -1001\./)).toBeVisible()

    const records = (await received(request)).filter((entry) => entry.path === '/fake-notify/telegram/bot***/sendMessage' && new Date(entry.time) >= since)
    expect(records.length).toBeGreaterThan(0)
    expect(records[0].body).toMatchObject({ chat_id: '-1001', parse_mode: 'HTML' })
    expect(JSON.stringify(await received(request))).not.toContain(token)

    await page.reload()
    await routeRow(page, 'e2e telegram').click()
    await expect(page.getByLabel('Bot token')).toHaveValue('')
    expect(await page.locator('body').innerText()).not.toContain(token)

    await startRoute(page, 'e2e telegram refused', 'Telegram')
    await page.getByLabel('Bot token').fill(token)
    await page.getByLabel('Chat ID').fill('-403')
    await page.getByRole('button', { name: 'Save route' }).click()
    await expect(page.getByText('Saved "e2e telegram refused".')).toBeVisible()
    await page.getByRole('button', { name: 'Send test' }).click()
    await expect(page.getByText('The bot cannot post to this chat. Add it to the chat first.')).toBeVisible()
    await openLog(page)
    await expect(page.getByRole('row').filter({ hasText: 'Failed' }).filter({ hasText: 'The bot cannot post to this chat. Add it to the chat first.' }).first()).toBeVisible()
    expect(ROUTE_PREFIX).toBe('e2e ')
  })
})

test.describe('omer', () => {
  test.use(asUser('omer'))
  test('a non-admin is told the page is for admins', async ({ page }) => {
    await page.goto('/settings/notifications')
    await expect(page.getByText('Only admins can manage notification routes.')).toBeVisible()
    await expect(page.getByRole('button', { name: 'Add route' })).toHaveCount(0)
  })
})
