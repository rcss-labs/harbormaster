import type { Page } from '@playwright/test'
import { asUser, expect, test } from './fixtures/test'
import { SVC, serviceUrl } from './fixtures/ids'
import { expectSheetWithinViewport, phoneViolations, watchPage } from './fixtures/phone'
import { measureBox, measureBoxes } from './helpers'

// Read-only phone checks of the Logs tab against the fake estate's history.
test.use({ ...asUser('sam'), viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true, deviceScaleFactor: 3 })

let problems: string[] = []
test.beforeEach(({ page }) => {
  problems = watchPage(page)
})

async function expectOwnLayoutClean(page: Page) {
  expect(problems, 'page errors, console errors and failing responses').toEqual([])
  expect(await phoneViolations(page), 'phone layout violations').toEqual([])
}

const paymentsLogs = serviceUrl(...SVC.paymentsApi, 'logs')

test('toolbar shows every Tail and Level option in full, inside the screen', async ({ page }) => {
  await page.goto(paymentsLogs)
  const tail = page.getByRole('group', { name: 'Tail' })
  const level = page.getByRole('radiogroup', { name: 'Level' })
  await expect(tail).toBeVisible()
  const options = [tail.getByRole('button', { name: 'Live' }), tail.getByRole('button', { name: 'Paused' }), level.getByRole('radio', { name: 'All' }), level.getByRole('radio', { name: 'Warn+' }), level.getByRole('radio', { name: 'Errors' })]
  for (const option of options) {
    await expect(option).toBeVisible()
    const box = await measureBox(option)
    expect(box.x, 'option starts on screen').toBeGreaterThanOrEqual(0)
    expect(box.x + box.width, 'option ends on screen').toBeLessThanOrEqual(390)
    expect(box.height, 'one line, 44 px target').toBeGreaterThanOrEqual(44)
    // The label fits its box: nothing is cut off inside the button.
    expect(await option.evaluate((el) => el.scrollWidth <= el.clientWidth + 1)).toBe(true)
  }
  await expect(page.getByLabel('Container')).toBeVisible()
  await expect(page.getByLabel('Search logs')).toBeVisible()
  await expectOwnLayoutClean(page)
})

test('Level filter and Tail toggle work on touch', async ({ page }) => {
  await page.goto(paymentsLogs)
  const level = page.getByRole('radiogroup', { name: 'Level' })
  await level.getByRole('radio', { name: 'Errors' }).tap()
  await expect(level.getByRole('radio', { name: 'Errors' })).toBeChecked()
  await level.getByRole('radio', { name: 'All' }).tap()
  const tail = page.getByRole('group', { name: 'Tail' })
  await tail.getByRole('button', { name: 'Paused' }).tap()
  await expect(tail.getByRole('button', { name: 'Paused' })).toHaveAttribute('aria-pressed', 'true')
  await tail.getByRole('button', { name: 'Live' }).tap()
  await expect(tail.getByRole('button', { name: 'Live' })).toHaveAttribute('aria-pressed', 'true')
})

test('Load older, a tap on a line opens its details sheet', async ({ page }) => {
  await page.goto(paymentsLogs)
  const log = page.getByRole('log')
  const rows = log.getByRole('button', { name: /\d\d:\d\d:\d\d/ })
  await expect(rows.first()).toBeVisible()
  const loadOlder = page.getByRole('button', { name: 'Load older' })
  if ((await loadOlder.count()) > 0) {
    await expect(loadOlder).toBeVisible()
  }
  await rows.first().tap()
  const sheet = page.getByRole('dialog', { name: 'Log line details' })
  await expect(sheet).toContainText('Log group')
  await expect(sheet).toContainText('Log stream')
  await expect(sheet).not.toContainText('Ingested')
  await expectSheetWithinViewport(page)
  await expectOwnLayoutClean(page)
  await page.keyboard.press('Escape')
  await expect(sheet).toBeHidden()
})

test('the log box runs to the bottom, within 24 px of the tab bar', async ({ page }) => {
  await page.goto(paymentsLogs)
  const box = page.getByRole('log')
  await expect(box).toBeVisible()
  const tabBar = page.getByRole('navigation', { name: 'Main' })
  const [logBox, tabBox] = await measureBoxes(box, tabBar)
  const gap = tabBox.y - (logBox.y + logBox.height)
  expect(gap, `log box ${JSON.stringify(logBox)} tab bar ${JSON.stringify(tabBox)}`).toBeGreaterThanOrEqual(0)
  expect(gap).toBeLessThanOrEqual(24)
  await expectOwnLayoutClean(page)
})
