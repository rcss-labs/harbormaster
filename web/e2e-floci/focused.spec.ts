import { expect, test } from '../e2e/fixtures/test'
import { resolveTargets } from './targets'

// Checks that need floci's real estate. At wave 0 the old UI fails the four that depend on v4 behaviour; each is
// marked test.fixme with the owning lane, and the owning wave-2 lane (2A) removes the marker. HM_FLOCI_UNFIXME=1
// runs them anyway, which is how the lead verifies them locally once the lanes are merged.
const fixmeUntil = (lane: string, what: string) => test.fixme(!process.env.HM_FLOCI_UNFIXME, `${what} (owner: ${lane}; removed by lane 2A)`)

const BOTTOM_TOLERANCE_PX = 24

test('terminal fills to the window bottom', async ({ page, request }) => {
  fixmeUntil('lane 1H', 'service Terminal tab does not yet fill the window height')
  const { serviceId } = await resolveTargets(request)
  for (const [width, height] of [
    [1440, 900],
    [3840, 2160],
  ]) {
    await page.setViewportSize({ width, height })
    await page.goto(`/services/${serviceId}/terminal`)
    const host = page.getByTestId('terminal-host')
    await expect(host).toBeVisible()
    const box = (await host.boundingBox())!
    const innerHeight = await page.evaluate(() => window.innerHeight)
    expect(innerHeight - (box.y + box.height), `terminal bottom gap at ${width}x${height}`).toBeLessThanOrEqual(BOTTOM_TOLERANCE_PX)
  }
})

test('logs fill to the window bottom', async ({ page, request }) => {
  fixmeUntil('lane 1H', 'service Logs tab does not yet fill the window height')
  const { serviceId } = await resolveTargets(request)
  for (const [width, height] of [
    [1440, 900],
    [3840, 2160],
  ]) {
    await page.setViewportSize({ width, height })
    await page.goto(`/services/${serviceId}/logs`)
    const log = page.getByRole('log')
    await expect(log).toBeVisible()
    const box = (await log.boundingBox())!
    const innerHeight = await page.evaluate(() => window.innerHeight)
    expect(innerHeight - (box.y + box.height), `log bottom gap at ${width}x${height}`).toBeLessThanOrEqual(BOTTOM_TOLERANCE_PX)
  }
})

test('only the scheduled actions panel degrades on Deploy', async ({ page, request }) => {
  fixmeUntil('lane 1B (API) and lane 1I (UI)', 'deploy settings are one request that fails whole until the partial response lands')
  const { serviceId } = await resolveTargets(request)
  await page.setViewportSize({ width: 1440, height: 900 })
  await page.goto(`/services/${serviceId}/deploy`)
  const main = page.getByRole('main')
  for (const heading of ['Containers', 'Task size', 'Service']) {
    await expect(main.getByRole('heading', { name: heading, exact: true }).first()).toBeVisible()
  }
  await expect(main.getByRole('table', { name: /Policies/ })).toBeVisible()
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
  const total = Number((await control.innerText()).match(/of (\d+)/)![1])
  await expect(popover.getByText('Auto scaling')).toHaveCount(total)
  await page.keyboard.press('Escape')
  await expect(page.locator('main [data-hm-banner]')).toHaveCount(0)
})

test('overview and deploy use two columns at 2560', async ({ page, request }) => {
  fixmeUntil('lane 1G (Overview) and lane 1I (Deploy)', 'the .hm-wide-split two-column layout is not built yet')
  const { serviceId } = await resolveTargets(request)
  await page.setViewportSize({ width: 2560, height: 1440 })
  for (const path of ['/', `/services/${serviceId}/deploy`]) {
    await page.goto(path)
    const split = page.locator('.hm-wide-split').first()
    await expect(split).toBeVisible()
    const children = split.locator('> *')
    expect(await children.count(), `${path}: columns`).toBeGreaterThanOrEqual(2)
    const first = (await children.nth(0).boundingBox())!
    const second = (await children.nth(1).boundingBox())!
    expect(second.x, `${path}: second column sits right of the first`).toBeGreaterThanOrEqual(first.x + first.width - 1)
    expect(Math.abs(second.y - first.y), `${path}: columns start at the same height`).toBeLessThanOrEqual(2)
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
