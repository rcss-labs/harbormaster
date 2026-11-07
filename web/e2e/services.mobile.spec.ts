import { asUser, expect, test } from './fixtures/test'
import { expectPhoneLayoutClean, expectSheetWithinViewport, watchPage } from './fixtures/phone'

// Read-only phone checks of the Services list (nothing is restarted: the confirm sheet is only opened and cancelled).
test.use({ viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true, deviceScaleFactor: 3 })

test.describe('services list on a phone as sam', () => {
  test.use(asUser('sam'))

  test('rows, action sheet, details and the restart confirm sheet stay inside 390', async ({ page }) => {
    watchPage(page)
    await page.goto('/services')
    const rows = page.getByRole('table', { name: 'Services' }).getByRole('row')
    await expect(rows).toHaveCount(15)
    const payments = rows.filter({ hasText: 'payments-api' })
    await expect(payments).toContainText('3/3 tasks')
    await expect(payments).toContainText(/CPU \d+ %/)
    await expect(payments).toContainText(/Mem \d+ %/)
    await expect(rows.filter({ hasText: 'edge-nginx' })).toContainText('CPU –')
    await expectPhoneLayoutClean(page)

    await page.getByRole('button', { name: 'Sort and fields' }).click()
    await expectSheetWithinViewport(page)
    await page.getByRole('button', { name: 'Done' }).click()

    await page.getByRole('button', { name: 'Actions for payments-api' }).click()
    const menu = page.getByRole('menu', { name: 'Actions for payments-api' })
    await expect(menu.getByRole('menuitem')).toHaveCount(7)
    await expectSheetWithinViewport(page)

    await menu.getByRole('menuitem', { name: 'Details' }).click()
    const details = page.getByRole('dialog', { name: 'payments-api' })
    await expect(details).toContainText('CPU')
    await expect(details).toContainText(/\d+ %/)
    await expectSheetWithinViewport(page)
    await details.getByRole('button', { name: 'Restart' }).click()
    await expect(page.getByRole('dialog', { name: 'Restart payments-api?' })).toBeVisible()
    await expectSheetWithinViewport(page)
    await page.getByRole('button', { name: 'Cancel' }).click()
  })

  test('empty state at 390 has Clear filters', async ({ page }) => {
    watchPage(page)
    await page.goto('/services?q=zzz-no-such-service')
    await expect(page.getByText('No services match these filters.')).toBeVisible()
    await expect(page.getByRole('button', { name: 'Clear filters' })).toBeVisible()
    await expectPhoneLayoutClean(page)
    await page.getByRole('button', { name: 'Clear filters' }).click()
    await expect(page.getByRole('table', { name: 'Services' }).getByRole('row')).toHaveCount(15)
  })
})

test.describe('services list on a phone as omer', () => {
  test.use(asUser('omer'))

  test('omer sees the disabled reason inside the sheet', async ({ page }) => {
    watchPage(page)
    await page.goto('/services?q=staging-web')
    const row = page.getByRole('table', { name: 'Services' }).getByRole('row').first()
    await row.getByRole('button', { name: /^Actions for / }).click()
    const restart = page.getByRole('menu').getByRole('menuitem', { name: /^Restart/ })
    await expect(restart).toBeDisabled()
    await expect(restart).toContainText('You need Operate on Staging')
    await expectSheetWithinViewport(page)
    await page.keyboard.press('Escape')
    expect(await page.getByRole('main').textContent()).not.toContain('payments-api')
  })
})
