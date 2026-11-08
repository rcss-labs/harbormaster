import type { Page } from '@playwright/test'
import { asUser, expect, test } from './fixtures/test'
import { expectPhoneLayoutClean, watchPage } from './fixtures/phone'

// Log search at phone width: the same run as the desktop spec, and nothing scrolls sideways (the check logs.mobile.spec.ts uses).
test.use({ ...asUser('sam'), viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true, deviceScaleFactor: 3 })

async function addService(page: Page, name: string) {
  await page.getByRole('combobox', { name: 'Add service' }).fill(name)
  await page.getByRole('listbox', { name: 'Services you can search' }).getByRole('option', { name: new RegExp(name) }).first().tap()
  await expect(page.getByRole('list', { name: 'Selected services' }).getByText(name, { exact: true })).toBeVisible()
  // The list stays open after a pick and would cover the controls below it.
  await page.keyboard.press('Escape')
}

test('runs a two-service search on a phone without horizontal scroll', async ({ page }) => {
  watchPage(page)
  await page.goto('/logs')
  await expect(page.getByRole('form', { name: 'Log search' })).toBeVisible()
  await addService(page, 'payments-api')
  await addService(page, 'catalog-api')
  await page.getByRole('group', { name: 'Range' }).getByRole('button', { name: '1 hour' }).tap()
  await page.getByLabel('Query').fill('filter @message like "GET /health"')
  await page.getByRole('button', { name: 'Run query' }).tap()

  await expect(page.locator('strong', { hasText: /matching lines?$/ })).toBeVisible()
  // The fake scans a few KB, so the cost reads `less than $0.01`; a real scan of 2 GB or more reads `about $...`.
  await expect(page.getByText(/scanned in [\d.]+ s · (about \$|less than \$)/)).toBeVisible()
  const lines = page.getByRole('list', { name: 'Matching lines' })
  await expect(lines.getByText('payments-api', { exact: true }).first()).toBeVisible()
  await expect(lines.getByText('catalog-api', { exact: true }).first()).toBeVisible()
  await expectPhoneLayoutClean(page)
})
