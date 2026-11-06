import { asUser, expect, test } from './fixtures/test'
import { SVC, serviceUrl } from './fixtures/ids'

// Read-only: runs in the observe project against the unmutated seed.
test.use(asUser('pavel'))

test('wide screens fill the content width', async ({ page }) => {
  await page.setViewportSize({ width: 2560, height: 1440 })
  await page.goto('/')
  const main = page.getByRole('main')
  await expect(main.getByRole('list', { name: 'Estate key figures' }).getByText('Tasks', { exact: true })).toBeVisible()

  // Grid is sidebar | main; the activity rail is a drawer and no longer docks: 2560 - 212 = 2348.
  const mainBox = await main.boundingBox()
  expect(mainBox!.width).toBeGreaterThan(2300)

  // The six KPI cells share one strip that spans the content width.
  const strip = main.getByRole('list', { name: 'Estate key figures' })
  const stripBox = (await strip.boundingBox())!
  const cells = strip.getByRole('listitem')
  await expect(cells).toHaveCount(6)
  const boxes = await Promise.all([0, 1, 2, 3, 4, 5].map((index) => cells.nth(index).boundingBox()))
  for (const box of boxes) expect(Math.abs(box!.width - boxes[0]!.width)).toBeLessThan(2)
  const last = boxes[5]!
  expect(Math.abs(last.x + last.width - (stripBox.x + stripBox.width))).toBeLessThanOrEqual(2)
})

test('the clusters table scrolls inside its panel at 920px without widening the page', async ({ page }) => {
  await page.setViewportSize({ width: 920, height: 800 })
  await page.goto('/clusters')
  const table = page.getByRole('table', { name: 'Clusters' })
  await expect(table.getByRole('row')).toHaveCount(7)
  const main = page.getByRole('main')
  expect(await main.evaluate((el) => el.scrollWidth - el.clientWidth)).toBeLessThanOrEqual(0)
  // The table keeps its minimum width, so it is wider than the panel that scrolls it.
  expect((await table.boundingBox())!.width).toBeGreaterThanOrEqual(1120)
})

test('logs and terminal fill the viewport height', async ({ page }) => {
  await page.setViewportSize({ width: 2560, height: 1440 })
  await page.goto(serviceUrl(...SVC.paymentsApi, 'logs'))
  const logs = page.getByRole('log')
  await expect(logs).toBeVisible()
  const logBox = (await logs.boundingBox())!
  expect(logBox.height).toBeGreaterThan(520)
  expect(1440 - (logBox.y + logBox.height)).toBeLessThan(60)

  await page.goto(serviceUrl(...SVC.paymentsApi, 'terminal'))
  const host = page.getByTestId('terminal-host')
  await expect(host).toBeVisible()
  expect((await host.boundingBox())!.height).toBeGreaterThan(480)
})

test('narrow viewport has no horizontal overflow', async ({ page }) => {
  await page.setViewportSize({ width: 900, height: 800 })
  await page.goto('/')
  const main = page.getByRole('main')
  await expect(main.getByRole('list', { name: 'Estate key figures' }).getByText('Tasks', { exact: true })).toBeVisible()
  // <main> is the scroll container (overflow: auto), so overflow shows up on it, never on the document.
  const overflow = await main.evaluate((el) => el.scrollWidth - el.clientWidth)
  expect(overflow).toBeLessThanOrEqual(0)
})

test('stop reason labels wrap instead of truncating', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 800 })
  await page.goto('/')
  const title = page.getByRole('main').getByText('Why tasks stopped, 3 days', { exact: true })
  await expect(title).toBeVisible()
  const labels = title.locator('xpath=ancestor::*[.//*[contains(@class,"hbarL")]][1]').locator('[class*="hbarL"]')
  const count = await labels.count()
  expect(count).toBeGreaterThan(0)
  for (let index = 0; index < count; index++) {
    const fits = await labels.nth(index).evaluate((el) => el.scrollWidth <= el.clientWidth)
    expect(fits, `label ${index} is truncated`).toBe(true)
  }
})

test('long tabs keep their full height inside the scrolling content', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 500 })
  for (const tab of ['', 'tasks']) {
    await page.goto(serviceUrl(...SVC.paymentsApi, tab || undefined))
    const panel = page.locator('[class*="tabPanel"]')
    await expect(panel).toBeVisible()
    const overflow = await panel.evaluate((el) => el.scrollHeight - el.clientHeight)
    expect(overflow, `tab "${tab}" panel is squashed`).toBeLessThanOrEqual(1)
  }
})
