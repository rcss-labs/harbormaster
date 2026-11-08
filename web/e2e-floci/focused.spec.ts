import { expect, test } from '../e2e/fixtures/test'
import { measureBox } from '../e2e/helpers'
import { resolveTargets } from './targets'

// Checks that need floci's real estate (a real ECS/Application Auto Scaling code path, not the fake).

const BOTTOM_TOLERANCE_PX = 24

test('terminal fills to the window bottom', async ({ page, request }) => {
  const { serviceId } = await resolveTargets(request)
  for (const [width, height] of [
    [1440, 900],
    [3840, 2160],
  ]) {
    await page.setViewportSize({ width, height })
    await page.goto(`/services/${serviceId}/terminal`)
    const host = page.getByTestId('terminal-host')
    await expect(host).toBeVisible()
    const box = await measureBox(host)
    const innerHeight = await page.evaluate(() => window.innerHeight)
    expect(innerHeight - (box.y + box.height), `terminal bottom gap at ${width}x${height}`).toBeLessThanOrEqual(BOTTOM_TOLERANCE_PX)
  }
})

test('logs fill to the window bottom', async ({ page, request }) => {
  const { serviceId } = await resolveTargets(request)
  for (const [width, height] of [
    [1440, 900],
    [3840, 2160],
  ]) {
    await page.setViewportSize({ width, height })
    await page.goto(`/services/${serviceId}/logs`)
    const log = page.getByRole('log')
    await expect(log).toBeVisible()
    const box = await measureBox(log)
    const innerHeight = await page.evaluate(() => window.innerHeight)
    expect(innerHeight - (box.y + box.height), `log bottom gap at ${width}x${height}`).toBeLessThanOrEqual(BOTTOM_TOLERANCE_PX)
  }
})

test('only the scheduled actions panel degrades on Deploy', async ({ page, request }) => {
  const { serviceId } = await resolveTargets(request)
  await page.setViewportSize({ width: 1440, height: 900 })
  await page.goto(`/services/${serviceId}/deploy`)
  const main = page.getByRole('main')
  for (const heading of ['Containers', 'Task size', 'Service']) {
    await expect(main.getByRole('heading', { name: heading, exact: true }).first()).toBeVisible()
  }
  await expect(main.getByRole('table', { name: 'Scaling policies' })).toBeVisible()
  const autoScaling = main.getByRole('heading', { name: 'Auto scaling' }).locator('xpath=ancestor::*[self::section or @role="region"][1]')
  const alerts = autoScaling.getByRole('alert')
  await expect(alerts).toHaveCount(1)
  await expect(alerts).toContainText('Scheduled actions could not be read')
  await expect(autoScaling.getByRole('button', { name: 'Retry' })).toBeVisible()
  await expect(page.getByRole('combobox', { name: 'Image tag' })).toBeEnabled()
})

test('freshness control counts core data', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  await page.goto('/')
  const control = page.getByRole('button', { name: /^\d+ of \d+ regions current$/ })
  await expect(control).toBeVisible()
  await control.click()
  const popover = page.getByRole('dialog').or(page.getByRole('region', { name: /fresh|current/i })).first()
  await expect(popover.getByText('Clusters and services').first()).toBeVisible()
  const [, current, total] = (await control.innerText()).match(/(\d+) of (\d+)/)!.map(Number)
  // A failing non-core tier (floci has no scheduled actions) must not label a region stale for core data.
  expect(current).toBe(total)
  await expect(popover.getByText('Auto scaling')).toHaveCount(total)
  await page.keyboard.press('Escape')
  await expect(page.locator('main [data-hm-banner]')).toHaveCount(0)
})

test('overview and deploy use two columns at 2560 and 3840', async ({ page, request }) => {
  const { serviceId } = await resolveTargets(request)
  // Overview's Triage layout is .hm-wide-split; Deploy's form is CSS columns in .hm-wide-form (the sticky aside is separate).
  const pages = [
    { path: '/', columns: '.hm-wide-split' },
    { path: `/services/${serviceId}/deploy`, columns: '.hm-wide-form' },
  ]
  for (const width of [2560, 3840]) {
    await page.setViewportSize({ width, height: 1440 })
    for (const { path, columns } of pages) {
      await page.goto(path)
      const split = page.locator(columns).first()
      await expect(split).toBeVisible()
      const children = split.locator('> *')
      await expect(children.nth(1)).toBeVisible()
      // The columns re-render as data arrives, so read both boxes in one step and poll until they are side by side.
      const layout = () =>
        split.evaluate((element) => {
          const [first, second] = [...element.children].map((child) => child.getBoundingClientRect())
          return { columns: element.children.length, rightOfFirst: second.x >= first.right - 1, topOffset: Math.abs(second.y - first.y) }
        })
      await expect.poll(layout, { message: `${path} at ${width}: two columns side by side` }).toMatchObject({ rightOfFirst: true })
      const settled = await layout()
      expect(settled.columns, `${path} at ${width}: columns`).toBeGreaterThanOrEqual(2)
      expect(settled.topOffset, `${path} at ${width}: columns start at the same height`).toBeLessThanOrEqual(2)
    }
  }
})

test('no page error on 390', async ({ page, request }) => {
  const { serviceId } = await resolveTargets(request)
  const errors: string[] = []
  page.on('pageerror', (error) => errors.push(error.message))
  await page.setViewportSize({ width: 390, height: 844 })
  for (const path of ['/', '/clusters', '/tasks', `/services/${serviceId}/overview`, `/services/${serviceId}/deploy`]) {
    await page.goto(path)
    await expect(page.getByRole('main')).toBeVisible()
    await page.waitForLoadState('networkidle')
  }
  expect(errors, 'uncaught page errors at 390px').toEqual([])
})

// Phone checks (mobile-v4 Lane M2A) on the floci estate. The terminal and live tail cannot connect here (no SSM sessions, no
// StartLiveTail), so these are about where things sit and which banners show, not about the session itself.
test.describe('phone', () => {
  test.use({ viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true, deviceScaleFactor: 3 })

  test('terminal and logs fill to the tab bar at 390', async ({ page, request }) => {
    const { serviceId } = await resolveTargets(request)
    const errors: string[] = []
    page.on('pageerror', (error) => errors.push(error.message))
    for (const [tab, region] of [
      ['terminal', page.getByTestId('terminal-host')],
      ['logs', page.getByRole('log')],
    ] as const) {
      await page.goto(`/services/${serviceId}/${tab}`)
      await expect(region).toBeVisible()
      const tabBar = page.getByRole('navigation', { name: 'Main' })
      await expect(tabBar).toBeVisible()
      const [box, bar] = [await measureBox(region), await measureBox(tabBar)]
      const gap = bar.y - (box.y + box.height)
      expect(gap, `${tab} bottom is above the tab bar`).toBeGreaterThanOrEqual(-1)
      expect(gap, `${tab} bottom is within ${BOTTOM_TOLERANCE_PX}px of the tab bar`).toBeLessThanOrEqual(BOTTOM_TOLERANCE_PX)
    }
    expect(errors, 'uncaught page errors at 390px').toEqual([])
  })

  test('lock banners at 390 as omer', async ({ browser, request }) => {
    const baseURL = test.info().project.use.baseURL as string
    const admin = await request.get('/api/me')
    const adminHeaders = { 'X-CSRF-Token': ((await admin.json()) as { csrf_token: string }).csrf_token }

    // omer is Operator on Dev. Give him something to see: tag one cluster Dev, and hand him a known password through a reset.
    const tags = ((await (await request.get('/api/tags')).json()).items ?? []) as { id: number; name: string }[]
    const dev = tags.find((tag) => tag.name === 'Dev')
    expect(dev, 'connect.sh creates the Dev tag').toBeTruthy()
    const clusters = ((await (await request.get('/api/clusters')).json()).items ?? []) as { id: string; name: string; services: { id: string; name: string }[] }[]
    const cluster = clusters.find((c) => c.name === 'background-jobs' && c.services.length > 0)
    expect(cluster, 'the seeded background-jobs cluster').toBeTruthy()
    expect((await request.put(`/api/tag-assignments/clusters/${cluster!.id}`, { data: { tag_id: dev!.id }, headers: adminHeaders })).ok()).toBe(true)
    const users = ((await (await request.get('/api/users')).json()).items ?? []) as { id: number; username: string }[]
    const omerId = users.find((user) => user.username === 'omer')!.id
    const reset = await request.post(`/api/users/${omerId}/reset-password`, { headers: adminHeaders })
    const temporary = ((await reset.json()) as { temporary_password: string }).temporary_password

    // A context of its own with no stored session: signing in over the admin's cookie would end the admin's session for the tests after this one.
    const context = await browser.newContext({ baseURL, storageState: { cookies: [], origins: [] }, viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true, deviceScaleFactor: 3 })
    try {
      const login = await context.request.post('/api/auth/login', { data: { username: 'omer', password: temporary } })
      expect(login.status(), 'omer sign-in with the temporary password').toBe(200)
      const omerCsrf = ((await (await context.request.get('/api/me')).json()) as { csrf_token: string }).csrf_token
      const fresh = `Omer-${Date.now()}-floci-phone`
      const change = await context.request.post('/api/auth/password', { data: { current: temporary, new: fresh }, headers: { 'X-CSRF-Token': omerCsrf } })
      expect(change.ok(), `omer sets his own password: ${change.status()} ${await change.text()}`).toBe(true)
      const page = await context.newPage()
      const errors: string[] = []
      page.on('pageerror', (error) => errors.push(error.message))
      const visible = ((await (await context.request.get('/api/services')).json()).items ?? []) as { id: string; name: string; tag: { name: string } }[]
      const service = visible.find((item) => item.tag.name === 'Dev')
      expect(service, 'omer sees a Dev service').toBeTruthy()
      await page.goto(`/services/${service!.id}/terminal`)
      await expect(page.getByText('You need').first()).toBeVisible()
      const overflow = await page.evaluate(() => document.scrollingElement!.scrollWidth - document.scrollingElement!.clientWidth)
      expect(overflow, 'no sideways scroll behind the lock banner').toBeLessThanOrEqual(1)
      await page.getByRole('button', { name: `More actions for ${service!.name}` }).click()
      await expect(page.getByRole('menu').getByRole('menuitem', { name: /^Restart/ })).toBeEnabled()
      expect(errors, 'uncaught page errors at 390px').toEqual([])
    } finally {
      await context.close()
    }
  })

  test('edge-proxy metrics tab shows Container Insights off at 390', async ({ page, request }) => {
    const services = ((await (await request.get('/api/services')).json()).items ?? []) as { id: string; name: string; cluster: { name: string } }[]
    const edge = services.find((service) => service.cluster.name === 'edge-proxy')
    expect(edge, `the seeded edge-proxy cluster has a service; services: ${services.map((s) => `${s.cluster.name}/${s.name}`).join(', ')}`).toBeTruthy()
    await page.goto(`/services/${edge!.id}/metrics`)
    await expect(page.getByText(/Container Insights is off for edge-proxy/)).toBeVisible()
    await expect(page.getByRole('heading', { name: 'CPU utilization' })).toHaveCount(0)
    const overflow = await page.evaluate(() => document.scrollingElement!.scrollWidth - document.scrollingElement!.clientWidth)
    expect(overflow, 'no sideways scroll').toBeLessThanOrEqual(1)
  })
})
