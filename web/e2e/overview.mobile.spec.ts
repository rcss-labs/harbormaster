import type { Page } from '@playwright/test'
import { asUser, expect, test } from './fixtures/test'
import { expectPhoneLayoutClean, expectSheetWithinViewport, watchPage } from './fixtures/phone'

// Read-only phone checks of the Overview: runs on the unmutated seed in the chromium-mobile project.
test.use({ viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true, deviceScaleFactor: 3 })

const main = (page: Page) => page.getByRole('main')

test.describe('overview on a phone as sam', () => {
  test.use(asUser('sam'))

  test('triage layout is clean and the Services tile agrees with the panel', async ({ page }) => {
    watchPage(page)
    await page.goto('/')
    const attention = main(page).getByRole('region', { name: 'Needs attention' })
    await expect(attention.getByTestId('alert-row').first()).toBeVisible()
    await expect(main(page).getByRole('region', { name: 'Estate map' }).getByRole('link')).toHaveCount(6)
    await page.waitForLoadState('networkidle')
    await expectPhoneLayoutClean(page)

    // The tile, the panel header and the Problems view all use the one needsAttention definition.
    const header = (await attention.getByText(/^\d+ services?/).textContent()) ?? ''
    const services = Number(header.match(/^(\d+)/)?.[1])
    const tile = main(page).getByRole('list', { name: 'Estate key figures' }).getByRole('link', { name: /^Services/ })
    await expect(tile).toContainText(`${services} need attention`)
  })

  test('an attention row opens a sheet with Logs, Tasks and Restart', async ({ page }) => {
    watchPage(page)
    await page.goto('/')
    const attention = main(page).getByRole('region', { name: 'Needs attention' })
    await expect(attention.getByTestId('alert-row').first()).toBeVisible()
    await attention.getByRole('button', { name: /^Actions for / }).first().click()
    const sheet = page.getByRole('dialog', { name: /^Actions for / })
    await expect(sheet.getByRole('link', { name: 'Logs' })).toBeVisible()
    await expect(sheet.getByRole('link', { name: 'Tasks' })).toBeVisible()
    await expect(sheet.getByRole('button', { name: /^Restart/ })).toBeVisible()
    await expectSheetWithinViewport(page)
    await page.keyboard.press('Escape')
    await expect(sheet).toBeHidden()
  })

  test('estate layout shows group headers and is clean', async ({ page }) => {
    watchPage(page)
    await page.goto('/')
    await main(page).getByRole('radio', { name: 'Estate' }).click()
    const estate = main(page).getByRole('region', { name: 'Services' })
    await expect(estate.getByRole('button', { expanded: true }).first()).toBeVisible()
    await expect(estate.getByRole('button', { name: /^Actions for / }).first()).toBeVisible()
    await page.waitForLoadState('networkidle')
    await expectPhoneLayoutClean(page)
    await estate.getByRole('button', { name: /^Actions for / }).first().click()
    await expectSheetWithinViewport(page)
    await page.keyboard.press('Escape')
  })
})

test.describe('overview on a phone as omer', () => {
  test.use(asUser('omer'))

  test('shows only his clusters and stays clean', async ({ page }) => {
    watchPage(page)
    await page.goto('/')
    await expect(main(page).getByRole('region', { name: 'Needs attention' })).toBeVisible()
    await expect(main(page).getByRole('region', { name: 'Estate map' }).getByRole('link')).toHaveCount(5)
    expect(await page.locator('body').textContent()).not.toContain('payments-api')
    await page.waitForLoadState('networkidle')
    await expectPhoneLayoutClean(page)
  })
})
