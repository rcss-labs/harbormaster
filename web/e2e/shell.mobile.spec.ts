import type { Page } from '@playwright/test'
import { asUser, expect, test } from './fixtures/test'
import { SVC, serviceUrl } from './fixtures/ids'
import { serviceId } from './helpers'

// Read-only phone shell checks. The viewport is set here so the spec is a phone spec whichever Playwright project picks it up.
test.use({ ...asUser('sam'), viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true, deviceScaleFactor: 3 })

const tabBar = (page: Page) => page.getByRole('navigation', { name: 'Main' })
const scopeChip = (page: Page) => page.getByRole('button', { name: /^Scope:/ })
const freshnessChip = (page: Page) => page.getByRole('button', { name: /^\d+ of \d+ regions current$/ })

interface Box {
  x: number
  y: number
  right: number
  bottom: number
  width: number
  height: number
}

/** Everything the shell chrome promises on a phone, read in one evaluate. Returns violation strings. */
async function shellChromeProblems(page: Page): Promise<string[]> {
  return page.evaluate(() => {
    const problems: string[] = []
    const box = (el: Element): Box => {
      const r = el.getBoundingClientRect()
      return { x: r.x, y: r.y, right: r.right, bottom: r.bottom, width: r.width, height: r.height }
    }
    const root = document.scrollingElement!
    if (root.scrollWidth > root.clientWidth + 1) problems.push(`document scrolls sideways: ${root.scrollWidth} > ${root.clientWidth}`)

    const header = document.querySelector('header')!
    const tabs = document.querySelector('nav[aria-label="Main"]')!
    for (const el of [...header.querySelectorAll('a, button')]) {
      const b = box(el)
      const hit36 = el.getAttribute('data-hm-hit') === '36'
      const label = el.getAttribute('aria-label') ?? el.textContent
      if (b.x < 0 || b.right > window.innerWidth) problems.push(`top bar control off-screen: ${label}`)
      if (b.height < (hit36 ? 36 : 44) || b.width < 36) problems.push(`top bar control too small: ${label} ${b.width}x${b.height}`)
    }
    const centres = [...header.querySelectorAll('a, button')].map((el) => box(el).y + box(el).height / 2)
    if (Math.max(...centres) - Math.min(...centres) > 2) problems.push('top bar wrapped onto more than one row')

    const tabBox = box(tabs)
    if (Math.abs(tabBox.bottom - window.innerHeight) > 1) problems.push('tab bar is not at the bottom edge')
    for (const tab of [...tabs.querySelectorAll('a')]) {
      const b = box(tab)
      const label = tab.querySelector('span:last-child')!
      const lineHeight = parseFloat(getComputedStyle(label).lineHeight)
      if (box(label).height >= 2 * lineHeight) problems.push(`tab label wraps: ${tab.getAttribute('aria-label')}`)
      if (b.height < 44) problems.push(`tab shorter than 44px: ${tab.getAttribute('aria-label')}`)
      const icon = tab.querySelector('svg')!
      const badge = tab.querySelector('span span:not(:last-child)')
      if (badge && badge.textContent) {
        const bb = box(badge)
        const ib = box(icon)
        const lb = box(label)
        if (bb.right > b.right || bb.x < b.x || bb.y < b.y) problems.push(`badge leaves its tab: ${tab.getAttribute('aria-label')}`)
        if (bb.bottom > lb.y) problems.push(`badge covers the tab label: ${tab.getAttribute('aria-label')}`)
        // The badge may touch the icon's corner but must not cover more than a third of the glyph.
        const overlapW = Math.max(0, Math.min(bb.right, ib.right) - Math.max(bb.x, ib.x))
        const overlapH = Math.max(0, Math.min(bb.bottom, ib.bottom) - Math.max(bb.y, ib.y))
        if (overlapW * overlapH > 0.2 * ib.width * ib.height) problems.push(`badge covers the icon: ${tab.getAttribute('aria-label')}`)
      }
    }

    // The page keeps clear of the tab bar once scrolled to the end.
    const main = document.querySelector('main')!
    main.scrollTop = main.scrollHeight
    window.scrollTo(0, root.scrollHeight)
    const visible = [...main.querySelectorAll('*')].filter((el) => {
      const b = box(el)
      return b.width > 0 && b.height > 0 && getComputedStyle(el).visibility !== 'hidden'
    })
    const lowest = Math.max(0, ...visible.map((el) => box(el).bottom))
    if (lowest > tabBox.y + 1) problems.push(`page content runs under the tab bar: ${lowest} > ${tabBox.y}`)
    return problems
  })
}

test('the tab bar navigates and marks the current tab', async ({ page }) => {
  await page.goto('/')
  await expect(tabBar(page)).toBeVisible()
  for (const [name, path] of [
    ['Services', '/services'],
    ['Clusters', '/clusters'],
    ['Tasks', '/tasks'],
    ['More', '/more'],
    ['Overview', '/'],
  ] as const) {
    const tab = tabBar(page).getByRole('link', { name: new RegExp(`^${name}`) })
    await tab.click()
    await expect(page).toHaveURL(new RegExp(`${path === '/' ? '/$' : `${path}(\\?|$)`}`))
    await expect(tab).toHaveAttribute('aria-current', 'page')
  }
})

test('the shell chrome is clean on Overview and More', async ({ page }) => {
  const errors: string[] = []
  page.on('pageerror', (error) => errors.push(error.message))
  for (const path of ['/', '/more']) {
    await page.goto(path)
    await expect(tabBar(page)).toBeVisible()
    await expect(freshnessChip(page)).toBeVisible()
    await expect.poll(() => shellChromeProblems(page)).toEqual([])
  }
  expect(errors).toEqual([])
})

test('More is current on the pages it leads to and the tab badges do not cover icons or labels', async ({ page }) => {
  await page.goto('/networks')
  await expect(tabBar(page).getByRole('link', { name: /^More/ })).toHaveAttribute('aria-current', 'page')
  // The seeded estate has a failing service, so the Overview badge exists; the chrome check covers the badge geometry.
  await expect(tabBar(page).getByRole('link', { name: /^Overview, \d+ need attention$/ })).toBeVisible()
  await expect.poll(() => shellChromeProblems(page)).toEqual([])
})

test('scope sheet applies a scope and clears it', async ({ page }) => {
  await page.goto('/clusters')
  await expect(scopeChip(page)).toHaveText('All')
  await scopeChip(page).click()
  const sheet = page.getByRole('dialog', { name: 'Scope' })
  await expect(sheet).toBeVisible()
  await sheet.getByLabel('Region filter').selectOption('us-east-1')
  await expect(scopeChip(page)).toHaveText('us-east-1')
  await expect(page).toHaveURL(/region=us-east-1/)
  const geometry = await sheet.evaluate((el) => {
    const r = el.getBoundingClientRect()
    const footer = [...el.querySelectorAll('button')].filter((b) => b.textContent === 'Clear scope' || b.textContent === 'Done').map((b) => b.getBoundingClientRect())
    return { x: r.x, right: r.right, top: r.top, bottom: r.bottom, innerWidth: window.innerWidth, innerHeight: window.innerHeight, footer: footer.map((f) => ({ bottom: f.bottom, right: f.right, height: f.height })) }
  })
  expect(geometry.x).toBeGreaterThanOrEqual(0)
  expect(geometry.right).toBeLessThanOrEqual(geometry.innerWidth)
  expect(geometry.top).toBeGreaterThanOrEqual(0)
  expect(geometry.bottom).toBeLessThanOrEqual(geometry.innerHeight + 1)
  expect(geometry.footer).toHaveLength(2)
  for (const button of geometry.footer) {
    expect(button.bottom).toBeLessThanOrEqual(geometry.innerHeight)
    expect(button.right).toBeLessThanOrEqual(geometry.innerWidth)
    expect(button.height).toBeGreaterThanOrEqual(44)
  }
  await sheet.getByRole('button', { name: 'Clear scope' }).click()
  await expect(scopeChip(page)).toHaveText('All')
  await sheet.getByRole('button', { name: 'Done' }).click()
  await expect(sheet).toHaveCount(0)
})

test('freshness sheet names the regions', async ({ page }) => {
  await page.goto('/')
  await expect(freshnessChip(page)).toHaveText(/^\d+\/\d+$/)
  await freshnessChip(page).click()
  const sheet = page.getByRole('dialog', { name: 'Data freshness' })
  await expect(sheet).toContainText('homelab-admin / eu-west-1')
  await expect(sheet).toContainText('regions current')
  await page.getByRole('button', { name: 'Close' }).click()
  await expect(sheet).toHaveCount(0)
})

test('the palette is full-screen and Cancel closes it', async ({ page }) => {
  await page.goto('/')
  await page.getByRole('button', { name: 'Search or run a command' }).click()
  const palette = page.getByRole('dialog', { name: 'Command palette' })
  await expect(palette).toBeVisible()
  const rect = await palette.evaluate((el) => {
    const r = el.getBoundingClientRect()
    return { x: r.x, y: r.y, width: r.width, height: r.height, innerWidth: window.innerWidth, innerHeight: window.innerHeight }
  })
  expect(rect.x).toBe(0)
  expect(rect.y).toBe(0)
  expect(rect.width).toBe(rect.innerWidth)
  expect(rect.height).toBe(rect.innerHeight)
  await expect(palette.getByText('Actions respect your permissions')).toBeVisible()
  await palette.getByRole('button', { name: 'Cancel' }).click()
  await expect(palette).toHaveCount(0)
})

test('More reaches every shell destination', async ({ page }) => {
  const payments = serviceId(...SVC.paymentsApi)
  await page.addInitScript((id) => {
    localStorage.setItem('harbormaster-pinned:sam', JSON.stringify([{ kind: 'service', id, name: 'payments-api', cluster: 'core-services' }]))
  }, payments)
  await page.goto('/more')
  await expect(page.getByText(/^Signed in as/)).toHaveText('Signed in as sam · Global admin')
  await expect(page.getByRole('region', { name: 'Signed in' }).getByText('Password', { exact: true })).toBeVisible()

  const links: Record<string, string> = {
    Networks: '/networks',
    Storage: '/storage',
    Analytics: '/analytics/delivery',
    General: '/settings/general',
    'AWS accounts': '/settings/accounts',
    'Users & permissions': '/settings/users',
    Tags: '/settings/tags',
    'Single sign-on': '/settings/sso',
    'Audit log': '/settings/audit',
    'My access': '/me/access',
    'Change password': '/password',
  }
  const main = page.getByRole('main')
  for (const [name, href] of Object.entries(links)) {
    await expect(main.getByRole('link', { name: new RegExp(`^${name}`) })).toHaveAttribute('href', href)
  }
  await expect(page.getByRole('region', { name: 'Pinned' }).getByRole('link', { name: /payments-api/ })).toHaveAttribute('href', serviceUrl(...SVC.paymentsApi, 'overview'))

  // Every row is a 44px target inside the screen.
  const rows = await main.evaluate((el) =>
    [...el.querySelectorAll('a, button')].map((row) => {
      const r = row.getBoundingClientRect()
      return { name: row.textContent ?? '', x: r.x, right: r.right, height: r.height }
    }),
  )
  expect(rows.length).toBeGreaterThan(12)
  for (const row of rows) {
    expect(row.x, row.name).toBeGreaterThanOrEqual(0)
    expect(row.right, row.name).toBeLessThanOrEqual(390)
    expect(row.height, row.name).toBeGreaterThanOrEqual(44)
  }

  await main.getByRole('button', { name: /^Activity/ }).click()
  const activity = page.getByRole('dialog', { name: 'Activity' })
  await expect(activity).toBeVisible()
  await activity.getByRole('button', { name: 'Close activity' }).click()
  await expect(activity).toHaveCount(0)

  const before = await page.locator('html').getAttribute('data-theme')
  await main.getByRole('button', { name: /^Theme/ }).click()
  await expect(page.locator('html')).not.toHaveAttribute('data-theme', before ?? '')
})

test('a non-admin user sees My access instead of the Admin rows', async ({ browser }) => {
  const context = await browser.newContext({ ...asUser('omer'), viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true })
  const page = await context.newPage()
  await page.goto('/more')
  await expect(page.getByRole('region', { name: 'Account' }).getByRole('link', { name: 'My access' })).toBeVisible()
  await expect(page.getByRole('region', { name: 'Admin' })).toHaveCount(0)
  await context.close()
})
