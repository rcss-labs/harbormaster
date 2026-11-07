import { asUser, expect, test } from './fixtures/test'
import type { Page } from '@playwright/test'
import { measureBox } from './helpers'

// Read-only: runs in the observe project against the unmutated seed. localStorage lasts inside one test only.
test.use(asUser('sam'))

const scopeButton = (page: Page) => page.getByRole('button', { name: /^Scope:/ })
const freshness = (page: Page) => page.getByRole('button', { name: /^\d+ of \d+ regions current$/ })

test('scope: save the current selection, clear it, apply it again', async ({ page }) => {
  await page.goto('/clusters')
  await scopeButton(page).click()
  const popover = page.getByRole('dialog', { name: 'Scope' })
  await popover.getByRole('button', { name: 'Tag filter', exact: true }).click()
  await popover.getByRole('group', { name: 'Tag filter options' }).getByRole('checkbox', { name: 'Prod' }).click()
  // Click outside the checkbox list to close it without closing the popover.
  await popover.getByText('Saved scopes').click()
  await expect(page).toHaveURL(/tag=3/)
  await expect(scopeButton(page)).toContainText('Prod')

  await popover.getByRole('button', { name: 'Save current' }).click()
  await popover.getByLabel('Scope name').fill('Prod only')
  await popover.getByRole('button', { name: 'Save', exact: true }).click()
  await expect(popover.getByRole('button', { name: 'Prod only', exact: true })).toBeVisible()

  // Clear the tag, then apply the saved scope.
  await popover.getByRole('button', { name: 'Tag filter', exact: true }).click()
  await popover.getByRole('group', { name: 'Tag filter options' }).getByRole('checkbox', { name: 'Prod' }).click()
  // Click outside the checkbox list to close it without closing the popover.
  await popover.getByText('Saved scopes').click()
  await expect(page).not.toHaveURL(/tag=3/)
  await expect(scopeButton(page)).toContainText('All tags')
  await popover.getByRole('button', { name: 'Prod only', exact: true }).click()
  await expect(page).toHaveURL(/tag=3/)
  await expect(scopeButton(page)).toContainText('Prod')
})

test('freshness control reads all regions current on the healthy estate', async ({ page }) => {
  await page.goto('/')
  await expect(page.getByRole('heading', { level: 1, name: 'Overview' })).toBeVisible()
  await expect(freshness(page)).toHaveText(/^(\d+) of \1 regions current$/)
  await freshness(page).click()
  // Per-tier detail ("Current: ...") needs the tiers from the poller backend lane, so only the regions are asserted here.
  await expect(page.getByRole('dialog', { name: 'Data freshness' })).toContainText('homelab-admin / eu-west-1')
})

test('density toggle persists across a reload', async ({ page }) => {
  await page.goto('/')
  await expect(page.locator('html')).not.toHaveAttribute('data-density', 'comfortable')
  await page.getByRole('button', { name: 'Toggle row density' }).click()
  await expect(page.locator('html')).toHaveAttribute('data-density', 'comfortable')
  await page.reload()
  await expect(page.locator('html')).toHaveAttribute('data-density', 'comfortable')
  await page.getByRole('button', { name: 'Toggle row density' }).click()
  await expect(page.locator('html')).not.toHaveAttribute('data-density', 'comfortable')
})

test('g s lands on /services and ? opens the shortcuts dialog', async ({ page }) => {
  await page.goto('/')
  await expect(page.getByRole('heading', { level: 1, name: 'Overview' })).toBeVisible()
  await page.keyboard.press('g')
  await page.keyboard.press('s')
  await expect(page).toHaveURL(/\/services(\?|$)/)
  await page.keyboard.press('?')
  const dialog = page.getByRole('dialog', { name: 'Keyboard shortcuts' })
  await expect(dialog).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(dialog).toHaveCount(0)
})

test('the activity drawer is closed until asked for and closes on Escape', async ({ page }) => {
  await page.setViewportSize({ width: 2560, height: 1000 })
  await page.goto('/')
  await expect(page.getByRole('heading', { level: 1, name: 'Overview' })).toBeVisible()
  await expect(page.getByRole('dialog', { name: 'Activity' })).toHaveCount(0)
  // main fills everything beside the sidebar: 2560 - 212.
  expect((await measureBox(page.getByRole('main'))).width).toBeGreaterThan(2300)
  await page.getByRole('button', { name: /^Activity/ }).click()
  await expect(page.getByRole('dialog', { name: 'Activity' })).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(page.getByRole('dialog', { name: 'Activity' })).toHaveCount(0)
})

test('crumbs never overlap the search button at 1024', async ({ page }) => {
  await page.setViewportSize({ width: 1024, height: 800 })
  await page.goto('/clusters')
  const crumbs = page.getByRole('navigation', { name: 'Breadcrumb' })
  // The nav is visible while still empty; the crumb text and the freshness control arrive later and reflow the top bar.
  // Wait for both, then measure the two boxes in one evaluate so a reflow cannot fall between the reads.
  await expect(crumbs).toHaveText('Clusters')
  await expect(page.getByRole('button', { name: /regions current$/ })).toBeVisible()
  const gapBeforeSearch = () =>
    page.evaluate(() => {
      const crumbBox = document.querySelector('nav[aria-label="Breadcrumb"]')!.getBoundingClientRect()
      const searchBox = [...document.querySelectorAll('button')].find((button) => /Search or run a command/.test(button.textContent ?? ''))!.getBoundingClientRect()
      return searchBox.left - crumbBox.right
    })
  await expect.poll(gapBeforeSearch).toBeGreaterThanOrEqual(-0.5)
  // The top bar itself never scrolls sideways.
  expect(await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)).toBeLessThanOrEqual(0)
})

test('collapsed sidebar keeps the account menu reachable', async ({ page }) => {
  await page.setViewportSize({ width: 1000, height: 800 })
  await page.goto('/')
  await page.getByRole('button', { name: /^Account menu for sam/ }).click()
  const menu = page.getByRole('group', { name: 'Account' })
  await expect(menu.getByRole('button', { name: 'Sign out' })).toBeVisible()
  await expect(menu.getByRole('link', { name: 'My access' })).toBeVisible()
})

test('at 390 the top bar is one row and every other control is reachable from More', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 800 })
  await page.goto('/')
  await expect(page.getByRole('heading', { level: 1, name: 'Overview' })).toBeVisible()
  await expect(freshness(page)).toBeVisible()

  // Read every box in one evaluate so a reflow cannot fall between the reads.
  const bar = await page.evaluate(() => {
    const header = document.querySelector('header')!
    const controls = [...header.querySelectorAll('a, button')].map((el) => {
      const r = el.getBoundingClientRect()
      return { name: el.getAttribute('aria-label') ?? '', x: r.x, right: r.right, centreY: r.y + r.height / 2, width: r.width, height: r.height }
    })
    return { controls, scrollWidth: document.documentElement.scrollWidth, headerHeight: header.getBoundingClientRect().height }
  })
  expect(bar.scrollWidth).toBeLessThanOrEqual(390)
  // Back or logo, scope, freshness and search: one row, nothing wrapped under another.
  expect(bar.controls.length).toBe(4)
  const centres = bar.controls.map((control) => control.centreY)
  expect(Math.max(...centres) - Math.min(...centres)).toBeLessThanOrEqual(2)
  for (const control of bar.controls) {
    expect(control.x).toBeGreaterThanOrEqual(0)
    expect(control.right).toBeLessThanOrEqual(390)
    expect(control.height).toBeGreaterThanOrEqual(36)
    expect(control.width).toBeGreaterThanOrEqual(36)
  }
  expect(bar.headerHeight).toBeLessThan(80)

  await freshness(page).click()
  const sheet = page.getByRole('dialog', { name: 'Data freshness' })
  await expect(sheet).toBeVisible()
  const sheetBox = await sheet.evaluate((el) => {
    const r = el.getBoundingClientRect()
    return { x: r.x, right: r.right, top: r.top, bottom: r.bottom, innerHeight: window.innerHeight }
  })
  expect(sheetBox.x).toBeGreaterThanOrEqual(0)
  expect(sheetBox.right).toBeLessThanOrEqual(390)
  expect(sheetBox.top).toBeGreaterThanOrEqual(0)
  expect(sheetBox.bottom).toBeLessThanOrEqual(sheetBox.innerHeight)
  await page.getByRole('button', { name: 'Close' }).click()

  // The sidebar, activity, theme and account controls moved to the More page; density and shortcut help are desktop-only.
  await expect(page.getByRole('button', { name: 'Toggle row density' })).toHaveCount(0)
  await expect(page.getByRole('button', { name: 'Keyboard shortcuts' })).toHaveCount(0)
  await page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: /^More/ }).click()
  await expect(page.getByRole('region', { name: 'Signed in' })).toBeVisible()
  await expect(page.getByRole('button', { name: /^Activity/ })).toBeVisible()
  await expect(page.getByRole('button', { name: /^Theme/ })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Sign out' })).toBeVisible()
})
