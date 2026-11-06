import type { Locator, Page } from '@playwright/test'
import { asUser, expect, test } from './fixtures/test'
import { SVC, serviceUrl } from './fixtures/ids'
import { measureBox, measureBoxes } from './helpers'

// The UI scale is CSS zoom on the root. These checks run in a real browser because jsdom has no layout: they prove the
// shell still fills the window, xterm still measures its cells, popovers and dialogs stay on screen, the sticky header
// sticks and charts follow the pointer. Chrome reports rects in viewport pixels under zoom, so they compare with
// innerWidth / innerHeight directly.
test.use(asUser('sam'))

/** Remembers a scale before the first script of every page runs. */
async function storeScale(page: Page, scale: string) {
  await page.addInitScript((value) => localStorage.setItem('harbormaster-scale', value), scale)
}

const dataScale = (page: Page) => page.evaluate(() => document.documentElement.dataset.scale ?? null)

/** The root, body and shell fill the window exactly, and nothing scrolls the document. */
async function expectFillsWindow(page: Page) {
  const read = await page.evaluate(() => {
    const root = document.documentElement
    const shell = document.querySelector('#root > div')!.getBoundingClientRect()
    return {
      innerWidth: window.innerWidth,
      innerHeight: window.innerHeight,
      scrollWidth: root.scrollWidth,
      scrollHeight: root.scrollHeight,
      body: document.body.getBoundingClientRect().toJSON(),
      shell: shell.toJSON(),
    }
  })
  expect(read.scrollWidth, 'horizontal scrollbar').toBeLessThanOrEqual(read.innerWidth)
  expect(read.scrollHeight, 'vertical scrollbar').toBeLessThanOrEqual(read.innerHeight)
  expect(Math.abs(read.shell.width - read.innerWidth), 'shell width gap').toBeLessThanOrEqual(1)
  expect(Math.abs(read.shell.height - read.innerHeight), 'shell height gap').toBeLessThanOrEqual(1)
  expect(Math.abs(read.body.height - read.innerHeight), 'body height gap').toBeLessThanOrEqual(1)
}

/** Every box lies inside the window. */
async function expectInsideWindow(page: Page, locators: Locator[], what: string) {
  const { innerWidth, innerHeight } = await page.evaluate(() => ({ innerWidth: window.innerWidth, innerHeight: window.innerHeight }))
  const boxes = await measureBoxes(...locators)
  boxes.forEach((box, index) => {
    const where = `${what} #${index} ${JSON.stringify(box)} in ${innerWidth}x${innerHeight}`
    expect(box.x, where).toBeGreaterThanOrEqual(-1)
    expect(box.y, where).toBeGreaterThanOrEqual(-1)
    expect(box.x + box.width, where).toBeLessThanOrEqual(innerWidth + 1)
    expect(box.y + box.height, where).toBeLessThanOrEqual(innerHeight + 1)
  })
}

test.describe('Auto', () => {
  test('at 3840 Auto renders the 2560 layout', async ({ page }) => {
    await page.setViewportSize({ width: 3840, height: 2160 })
    await page.goto('/')
    const main = page.getByRole('main')
    await expect(main.getByRole('list', { name: 'Estate key figures' }).getByText('Tasks', { exact: true })).toBeVisible()
    expect(await dataScale(page)).toBe('150')
    await expectFillsWindow(page)

    // 3840 / 1.5 = 2560 layout px: the main column is 2560 minus the 212 px sidebar, as in layout.spec.ts at 2560.
    const layoutWidth = await main.evaluate((element) => element.clientWidth)
    expect(Math.abs(layoutWidth - (2560 - 212))).toBeLessThanOrEqual(40)
    await expect(page.getByRole('button', { name: /sidebar$/i })).toHaveAttribute('aria-expanded', 'true')

    // The two-column split of the wide overview is side by side.
    const split = page.locator('.hm-wide-split').first()
    await expect(split).toBeVisible()
    const columns = split.locator(':scope > *')
    expect(await columns.count()).toBeGreaterThanOrEqual(2)
    const [first, second] = await measureBoxes(columns.nth(0), columns.nth(1))
    expect(second.x).toBeGreaterThan(first.x + first.width - 2)
    expect(Math.abs(second.y - first.y)).toBeLessThan(2)
  })

  test('at 2560 no scale is applied', async ({ page }) => {
    await page.setViewportSize({ width: 2560, height: 1440 })
    await page.goto('/')
    await expect(page.getByRole('main').getByRole('list', { name: 'Estate key figures' })).toBeVisible()
    expect(await dataScale(page)).toBeNull()
    await expectFillsWindow(page)
    expect((await measureBox(page.getByRole('main'))).width).toBeGreaterThan(2300)
  })

  test('resizing a window across 3000 px switches Auto', async ({ page }) => {
    await page.setViewportSize({ width: 2560, height: 1440 })
    await page.goto('/')
    await expect(page.getByRole('main')).toBeVisible()
    expect(await dataScale(page)).toBeNull()
    await page.setViewportSize({ width: 3840, height: 2160 })
    await expect.poll(() => dataScale(page)).toBe('150')
    await expectFillsWindow(page)
    await page.setViewportSize({ width: 2560, height: 1440 })
    await expect.poll(() => dataScale(page)).toBeNull()
    await expectFillsWindow(page)
  })
})

test.describe('the control', () => {
  test('a chosen scale applies at once and survives a reload', async ({ page }) => {
    await page.setViewportSize({ width: 3840, height: 2160 })
    await page.goto('/')
    await page.getByRole('button', { name: 'UI scale' }).click()
    const popover = page.getByRole('group', { name: 'UI scale' })
    await expect(popover.getByRole('button', { name: 'Auto' })).toHaveAttribute('aria-pressed', 'true')
    await expect(popover.getByText('Auto uses 150 % on windows 3000 px and wider.')).toBeVisible()

    await popover.getByRole('button', { name: '200 %' }).click()
    expect(await dataScale(page)).toBe('200')
    await expectFillsWindow(page)
    await page.reload()
    await expect(page.getByRole('main')).toBeVisible()
    expect(await dataScale(page)).toBe('200')
    expect(await page.evaluate(() => localStorage.getItem('harbormaster-scale'))).toBe('200')

    await page.getByRole('button', { name: 'UI scale' }).click()
    await page.getByRole('group', { name: 'UI scale' }).getByRole('button', { name: '100 %' }).click()
    expect(await dataScale(page)).toBeNull()
    await expectFillsWindow(page)
    await page.reload()
    await expect(page.getByRole('main')).toBeVisible()
    expect(await dataScale(page)).toBeNull()
  })
})

test.describe('at 150 %', () => {
  test.beforeEach(async ({ page }) => {
    await page.setViewportSize({ width: 3840, height: 2160 })
  })

  test('terminal cols at 150 % match the host width', async ({ page }) => {
    await page.goto(serviceUrl(...SVC.paymentsApi, 'terminal'))
    const host = page.getByTestId('terminal-host')
    await expect(host).toBeVisible()
    await page.getByRole('button', { name: 'Connect' }).click()
    await expect(page.locator('.xterm-rows')).toContainText('Starting session with SessionId: ecs-execute-command-', { timeout: 15_000 })
    expect(await dataScale(page)).toBe('150')

    // xterm sizes the grid from its measured cell; if zoom broke that measurement the screen would be far from the host width.
    const read = await page.evaluate(() => {
      const hostElement = document.querySelector('[data-testid="terminal-host"]')!
      const screen = document.querySelector('.xterm-screen')!
      const row = document.querySelector('.xterm-rows > div')!
      return { host: hostElement.getBoundingClientRect().toJSON(), screen: screen.getBoundingClientRect().toJSON(), row: row.getBoundingClientRect().height }
    })
    // One column of the 150 % layout is about 8 viewport px; allow a column plus the scrollbar.
    const slack = 8 * 1.5 + 20
    expect(read.screen.width, JSON.stringify(read)).toBeLessThanOrEqual(read.host.width + 1)
    expect(read.host.width - read.screen.width, JSON.stringify(read)).toBeLessThanOrEqual(slack)
    // Rows fill the host height within one row.
    expect(read.host.height - read.screen.height, JSON.stringify(read)).toBeLessThanOrEqual(read.row + 1)
    expect(read.screen.height).toBeLessThanOrEqual(read.host.height + 1)
  })

  test('logs and terminal reach the bottom of the window', async ({ page }) => {
    await page.goto(serviceUrl(...SVC.paymentsApi, 'logs'))
    const logs = page.getByRole('log')
    await expect(logs).toBeVisible()
    expect(await dataScale(page)).toBe('150')
    const logBox = await measureBox(logs)
    expect(2160 - (logBox.y + logBox.height)).toBeLessThan(24 * 1.5 + 2)
    expect(2160 - (logBox.y + logBox.height)).toBeGreaterThanOrEqual(0)

    await page.goto(serviceUrl(...SVC.paymentsApi, 'terminal'))
    const host = page.getByTestId('terminal-host')
    await expect(host).toBeVisible()
    const hostBox = await measureBox(host)
    expect(2160 - (hostBox.y + hostBox.height)).toBeLessThan(24 * 1.5 + 2)
    expect(2160 - (hostBox.y + hostBox.height)).toBeGreaterThanOrEqual(0)
  })

  test('the service sticky header still sticks', async ({ page }) => {
    // A shorter window so the page scrolls.
    await page.setViewportSize({ width: 2560, height: 700 })
    await storeScale(page, '150')
    await page.goto(serviceUrl(...SVC.paymentsApi, 'overview'))
    const header = page.locator('[class*="stickyHead"]')
    await expect(header).toBeVisible()
    const main = page.getByRole('main')
    expect(await dataScale(page)).toBe('150')
    const scrollable = await main.evaluate((element) => element.scrollHeight - element.clientHeight)
    expect(scrollable, 'the page must scroll for this check').toBeGreaterThan(200)
    await main.evaluate((element) => element.scrollBy(0, 600))
    await expect.poll(() => main.evaluate((element) => element.scrollTop)).toBeGreaterThan(200)
    const [headerBox, mainBox] = await measureBoxes(header, main)
    expect(headerBox.y).toBeGreaterThanOrEqual(0)
    expect(Math.abs(headerBox.y - mainBox.y), `header ${JSON.stringify(headerBox)} main ${JSON.stringify(mainBox)}`).toBeLessThanOrEqual(2)
  })

  test('chart crosshair lines up with the pointer', async ({ page }) => {
    await page.goto('/analytics')
    const plot = page.locator('[class*="tsPlot"]').first()
    await expect(plot).toBeVisible()
    expect(await dataScale(page)).toBe('150')
    const box = await measureBox(plot)
    const pointerX = box.x + box.width * 0.85
    await page.mouse.move(pointerX, box.y + box.height / 2)
    const crosshair = plot.locator('[class*="tsCross"]')
    await expect(crosshair).toBeVisible()
    const cross = await measureBox(crosshair)
    // The crosshair snaps to the nearest sample; the plot has at most a few hundred, so it is within a few pixels.
    expect(cross.x).toBeGreaterThan(box.x + box.width * 2 / 3)
    expect(Math.abs(cross.x - pointerX)).toBeLessThanOrEqual(Math.max(8, box.width / 40))
    const tooltip = plot.getByRole('tooltip')
    await expect(tooltip).toBeVisible()
    await expectInsideWindow(page, [tooltip], 'tooltip')
  })
})

test.describe('at 200 %', () => {
  test.beforeEach(async ({ page }) => {
    await page.setViewportSize({ width: 1920, height: 1080 })
    await storeScale(page, '200')
  })

  test('popover stays inside the viewport at 200 %', async ({ page }) => {
    await page.goto('/')
    await expect(page.getByRole('main').getByRole('list', { name: 'Estate key figures' })).toBeVisible()
    expect(await dataScale(page)).toBe('200')
    await expectFillsWindow(page)

    await page.getByRole('button', { name: /^Scope:/ }).click()
    await expectInsideWindow(page, [page.getByRole('dialog', { name: 'Scope' })], 'scope popover')
    await page.keyboard.press('Escape')

    await page.getByRole('button', { name: /regions current/ }).click()
    await expectInsideWindow(page, [page.getByRole('dialog', { name: 'Data freshness' })], 'freshness popover')
    await page.keyboard.press('Escape')

    await page.getByRole('button', { name: 'UI scale' }).click()
    await expectInsideWindow(page, [page.getByRole('group', { name: 'UI scale' })], 'scale popover')
    await page.keyboard.press('Escape')

    await page.getByRole('button', { name: /^Activity/ }).click()
    await expectInsideWindow(page, [page.getByRole('dialog', { name: 'Activity' })], 'activity drawer')
  })

  test('the palette opens centred within the viewport at 200 %', async ({ page }) => {
    await page.setViewportSize({ width: 2560, height: 1440 })
    await page.goto('/')
    await expect(page.getByRole('main')).toBeVisible()
    await page.keyboard.press('Control+k')
    const palette = page.getByRole('dialog', { name: /command|search/i })
    await expect(palette).toBeVisible()
    await expectInsideWindow(page, [palette], 'palette')
    const box = await measureBox(palette)
    expect(Math.abs(box.x + box.width / 2 - 2560 / 2)).toBeLessThanOrEqual(2)
  })

  // The palette backdrop's `padding-top: 12vh` is counted in zoomed px, so shell.module.css divides it by var(--hm-zoom, 1).
  test('the palette top is about 12 % of the window height at 200 %', async ({ page }) => {
    await page.setViewportSize({ width: 2560, height: 1440 })
    await page.goto('/')
    await page.keyboard.press('Control+k')
    const box = await measureBox(page.getByRole('dialog', { name: /command|search/i }))
    expect(box.y).toBeLessThanOrEqual(1440 * 0.16)
  })

  test('the keyboard help dialog fits the viewport at 200 %', async ({ page }) => {
    await page.goto('/')
    await expect(page.getByRole('main')).toBeVisible()
    await page.getByRole('button', { name: 'Keyboard shortcuts' }).click()
    await expectInsideWindow(page, [page.getByRole('dialog')], 'help dialog')
  })

  test('the pending changes aside fits at 200 %', async ({ page }) => {
    await page.goto(serviceUrl(...SVC.paymentsApi, 'deploy'))
    const aside = page.getByRole('region', { name: 'Pending changes' })
    await expect(aside).toBeVisible()
    await expectInsideWindow(page, [aside], 'pending changes aside')
  })

  // Viewport units are counted in zoomed px, so a cap written as 100vh or 70dvh is twice the window at 200 %; the rules
  // (PendingChangesAside, taskDefinition, Drawer, Popover, palette) divide by var(--hm-zoom, 1).
  test('viewport-unit caps stay within the window at 200 %', async ({ page }) => {
    await page.goto(serviceUrl(...SVC.paymentsApi, 'deploy'))
    const cap = await page.getByRole('region', { name: 'Pending changes' }).evaluate((element) => parseFloat(getComputedStyle(element).maxHeight) * 2)
    expect(cap).toBeLessThanOrEqual(1080)
  })
})
