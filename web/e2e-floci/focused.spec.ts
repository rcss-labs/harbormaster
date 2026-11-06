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
