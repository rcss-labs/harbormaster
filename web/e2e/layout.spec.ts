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

// Header and body cells must share one set of column tracks, and the pinned actions column must never hide a cell
// that cannot be scrolled into view.
const tableTargets = [
  { name: 'Services', path: () => '/services', table: 'Services' },
  { name: 'service Tasks tab', path: () => serviceUrl(...SVC.paymentsApi, 'tasks'), table: 'Tasks' },
]

for (const target of tableTargets) {
  for (const width of [390, 1440, 3840]) {
    test(`${target.name} table keeps columns aligned and uncovered at ${width}px`, async ({ page }) => {
      await page.setViewportSize({ width, height: 900 })
      await page.goto(target.path())
      const table = page.getByRole('table', { name: target.table })
      const firstRow = table.getByRole('row').nth(1)
      await expect(firstRow).toBeVisible()

      // The first cell (the name or task id) is what the user sees at its centre, not the sticky actions cell.
      const firstCell = firstRow.getByRole('cell').first()
      const covered = await firstCell.evaluate((cell) => {
        const box = cell.getBoundingClientRect()
        const top = document.elementFromPoint(box.x + box.width / 2, box.y + box.height / 2)
        return !top || !cell.contains(top)
      })
      expect(covered, 'first cell is covered').toBe(false)

      // Every column header sits over its cells.
      const headerCells = table.getByRole('row').first().getByRole('columnheader')
      const bodyCells = firstRow.getByRole('cell')
      const count = await headerCells.count()
      expect(await bodyCells.count()).toBe(count)
      for (let index = 0; index < count; index++) {
        const header = (await headerCells.nth(index).boundingBox())!
        const cell = (await bodyCells.nth(index).boundingBox())!
        expect(Math.abs(header.x - cell.x), `column ${index} header x ${header.x} vs cell x ${cell.x}`).toBeLessThanOrEqual(2)
      }

      // Scrolled fully right, the last data column ends where the pinned actions cell begins.
      const clear = await firstRow.evaluate((row) => {
        let scroller: HTMLElement | null = row.parentElement
        while (scroller && getComputedStyle(scroller).overflowX === 'visible') scroller = scroller.parentElement
        if (scroller) scroller.scrollLeft = scroller.scrollWidth
        const cells = row.querySelectorAll('[role="cell"]')
        const actions = cells[cells.length - 1].getBoundingClientRect()
        const lastData = cells[cells.length - 2].getBoundingClientRect()
        return actions.left - lastData.right
      })
      expect(clear, 'last data column is under the actions cell').toBeGreaterThanOrEqual(-1)
    })
  }
}

test.describe('row action menu at 390', () => {
  test.use(asUser('pavel'))

  test('every action of the last row stays clickable', async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 })
    await page.goto('/services')
    const lastRowActions = page.getByRole('button', { name: /actions/i }).last()
    await lastRowActions.scrollIntoViewIfNeeded()
    await lastRowActions.click()
    const items = page.getByRole('menu').getByRole('menuitem')
    expect(await items.count()).toBeGreaterThan(0)
    // A trial click runs Playwright's hit-target check without acting, so a clipped or covered item fails here.
    for (const item of await items.all()) {
      if (await item.isEnabled()) await item.click({ trial: true, timeout: 5_000 })
    }
  })
})

// A segment label must never wrap inside its box: segments grow to fit and the group wraps onto more rows instead.
test.describe('segmented filters', () => {
  test('at 390 labels stay on one line, inside the panel, with every option visible', async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 })
    await page.goto('/services')
    // Every segmented control: the radiogroup filters and the toggle group of saved views share one component.
    const groups = page.locator('[class*="seg"]:has(> button)')
    await expect(groups.first()).toBeVisible()
    for (const name of ['My pins', 'In progress', 'Steady', 'Scaled to 0']) {
      await expect(groups.locator('button', { hasText: new RegExp(`^${name}`) })).toBeVisible()
    }
    const shortHeight = (await groups.locator('button', { hasText: /^All/ }).first().boundingBox())!.height

    const results = await groups.evaluateAll((elements) =>
      elements.map((group) => {
        const panel = group.closest('[class*="panel"]') ?? document.body
        const panelRight = panel.getBoundingClientRect().right
        const radios = [...group.querySelectorAll('button')].map((radio) => {
          const box = radio.getBoundingClientRect()
          return { text: radio.textContent ?? '', height: box.height, right: box.right }
        })
        return { name: group.getAttribute('aria-label'), radios, panelRight, scrolls: group.scrollWidth - group.clientWidth }
      }),
    )
    expect(results.length).toBeGreaterThan(0)
    for (const group of results) {
      expect(group.scrolls, `${group.name} scrolls horizontally`).toBeLessThanOrEqual(0)
      for (const radio of group.radios) {
        expect(Math.abs(radio.height - shortHeight), `"${radio.text}" wraps (h=${radio.height})`).toBeLessThanOrEqual(1)
        expect(radio.right, `"${radio.text}" leaves the panel`).toBeLessThanOrEqual(group.panelRight + 0.5)
      }
    }
  })

  test('at 1440 the status filter keeps one row', async ({ page }) => {
    await page.setViewportSize({ width: 1440, height: 900 })
    await page.goto('/services')
    const radios = page.getByRole('radiogroup', { name: 'Status' }).getByRole('radio')
    await expect(radios.first()).toBeVisible()
    const tops = await radios.evaluateAll((els) => els.map((el) => Math.round(el.getBoundingClientRect().top)))
    expect(new Set(tops).size).toBe(1)
  })
})
