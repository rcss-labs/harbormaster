import { asUser, expect, test } from './fixtures/test'
import { SVC, serviceUrl } from './fixtures/ids'

// Read-only: runs in the observe project against the unmutated seed.
test.use(asUser('pavel'))

test('wide screens fill the content width', async ({ page }) => {
  await page.setViewportSize({ width: 2560, height: 1440 })
  await page.goto('/')
  const main = page.getByRole('main')
  await expect(main.getByText('Tasks running', { exact: true })).toBeVisible()

  const mainBox = await main.boundingBox()
  expect(mainBox!.width).toBeGreaterThan(2200)

  // Clusters, Services, Tasks running and the average-use tile share the first stat grid.
  const grid = main.getByText('Clusters', { exact: true }).locator('../..')
  const gridBox = (await grid.boundingBox())!
  const tiles = grid.locator(':scope > *')
  await expect(tiles).toHaveCount(4)
  const boxes = await Promise.all([0, 1, 2, 3].map((index) => tiles.nth(index).boundingBox()))
  for (const box of boxes) expect(Math.abs(box!.width - boxes[0]!.width)).toBeLessThan(2)
  const last = boxes[3]!
  expect(Math.abs(last.x + last.width - (gridBox.x + gridBox.width))).toBeLessThanOrEqual(2)
})

test('cluster cards grow more columns on wide screens', async ({ page }) => {
  await page.setViewportSize({ width: 2560, height: 1440 })
  await page.goto('/clusters')
  const cards = page.getByRole('main').getByRole('article')
  await expect(cards).toHaveCount(6)
  const rows = new Set<number>()
  for (let index = 0; index < 6; index++) rows.add(Math.round((await cards.nth(index).boundingBox())!.y))
  expect(rows.size).toBe(1)
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

test('narrow viewport wraps the tiles without horizontal overflow', async ({ page }) => {
  await page.setViewportSize({ width: 900, height: 800 })
  await page.goto('/')
  const main = page.getByRole('main')
  await expect(main.getByText('Tasks running', { exact: true })).toBeVisible()
  const overflow = await page.evaluate(() => document.scrollingElement!.scrollWidth - window.innerWidth)
  expect(overflow).toBeLessThanOrEqual(0)

  const grid = main.getByText('Clusters', { exact: true }).locator('../..')
  const tiles = grid.locator(':scope > *')
  const ys = new Set<number>()
  for (let index = 0; index < (await tiles.count()); index++) ys.add(Math.round((await tiles.nth(index).boundingBox())!.y))
  expect(ys.size).toBeGreaterThan(1)
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
