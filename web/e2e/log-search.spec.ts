import type { Page } from '@playwright/test'
import { asUser, expect, test } from './fixtures/test'

// Log search against the fake estate: `serve --fake-aws` answers `filter @message like "text"` from its seeded log history.
// sam is a global admin, so every service is searchable. Saving a query changes sam's saved list, so this runs with the flows.
test.use(asUser('sam'))

const QUERY = 'filter @message like "GET /health"'

/** Adds a service to the picker by typing part of its name and choosing the first match. */
async function addService(page: Page, name: string) {
  const picker = page.getByRole('combobox', { name: 'Add service' })
  await picker.fill(name)
  await page.getByRole('listbox', { name: 'Services you can search' }).getByRole('option', { name: new RegExp(name) }).first().click()
  await expect(page.getByRole('list', { name: 'Selected services' }).getByText(name, { exact: true })).toBeVisible()
  // The list stays open after a pick and would cover the controls below it.
  await page.keyboard.press('Escape')
}

async function runHealthSearch(page: Page) {
  await page.goto('/logs')
  await expect(page.getByRole('form', { name: 'Log search' })).toBeVisible()
  // payments-api (core-services) and catalog-api (eu-apps): two services in different clusters.
  await addService(page, 'payments-api')
  await addService(page, 'catalog-api')
  await page.getByRole('group', { name: 'Range' }).getByRole('button', { name: '1 hour' }).click()
  await page.getByLabel('Query').fill(QUERY)
  await page.getByRole('button', { name: 'Run query' }).click()
}

test('searches two services, shows the cost, and a line opens that service Logs tab', async ({ page }) => {
  await runHealthSearch(page)
  const count = page.locator('strong', { hasText: /matching lines?$/ })
  await expect(count).toBeVisible()
  expect(Number((await count.innerText()).split(' ')[0].replace(/,/g, '')), 'matching lines').toBeGreaterThan(0)
  // The fake scans a few KB, so the cost reads `less than $0.01`; a real scan of 2 GB or more reads `about $...`.
  await expect(page.getByText(/scanned in [\d.]+ s · (about \$|less than \$)/)).toBeVisible()

  const lines = page.getByRole('list', { name: 'Matching lines' })
  await expect(lines.getByText('payments-api', { exact: true }).first()).toBeVisible()
  await expect(lines.getByText('catalog-api', { exact: true }).first()).toBeVisible()

  await lines.getByRole('link').filter({ hasText: 'catalog-api' }).first().click()
  await expect(page).toHaveURL(/\/services\/[^/]+\/logs/)
  await expect(page.getByText(/Showing logs up to/)).toBeVisible()
})

test('a saved query survives a reload and restores both services', async ({ page }) => {
  await runHealthSearch(page)
  await page.getByRole('button', { name: 'Save this query' }).click()
  const dialog = page.getByRole('dialog', { name: 'Save this query' })
  await dialog.getByLabel('Name').fill('health')
  await dialog.getByRole('button', { name: 'Save' }).click()
  await expect(dialog).toBeHidden()

  await page.goto('/logs')
  await page.getByRole('button', { name: 'Load health' }).click()
  const chips = page.getByRole('list', { name: 'Selected services' })
  await expect(chips.getByText('payments-api', { exact: true })).toBeVisible()
  await expect(chips.getByText('catalog-api', { exact: true })).toBeVisible()
  await expect(page.getByLabel('Query')).toHaveValue(QUERY)
})
