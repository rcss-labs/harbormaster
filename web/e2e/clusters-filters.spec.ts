import { asUser, expect, test } from './fixtures/test'

// Read-only: runs in the observe project. Status, name and the shared account scope on the Clusters page.

test.describe('clusters filter bar as sam', () => {
  test.use(asUser('sam'))

  // The table has a header row, so N clusters are N + 1 rows.
  test('environment, status and name filters', async ({ page }) => {
    await page.goto('/clusters')
    const rows = page.getByRole('table', { name: 'Clusters' }).getByRole('row')
    await expect(rows).toHaveCount(7)
    // Worst first by default.
    await expect(rows.nth(1)).toContainText('background-jobs')

    await page.getByRole('radiogroup', { name: 'Status' }).getByRole('radio', { name: /^Problems/ }).click()
    await expect(rows).toHaveCount(3)
    await expect(page.getByRole('row', { name: /background-jobs/ })).toBeVisible()
    await expect(page).toHaveURL(/status=problems/)

    await page.getByRole('searchbox', { name: 'Filter clusters' }).fill('core')
    await expect(page.getByText('No clusters match these filters.')).toBeVisible()

    await page
      .getByText('No clusters match these filters.')
      .getByRole('button', { name: 'Clear filters' })
      .click()
    await expect(rows).toHaveCount(7)

    await page.getByRole('searchbox', { name: 'Filter clusters' }).fill('eu')
    // Header row + eu-apps (name, eu-west-1) + edge-proxy (eu-central-1): the filter also matches the region.
    await expect(rows).toHaveCount(3)
    await expect(page.getByRole('row', { name: /eu-apps/ })).toBeVisible()
    await expect(page.getByRole('row', { name: /edge-proxy/ })).toBeVisible()

    await page.goto('/clusters?env=staging&q=web')
    await expect(rows).toHaveCount(2)
    await expect(page.getByRole('row', { name: /staging-web/ })).toBeVisible()
  })

  test('the top bar account select narrows the table', async ({ page }) => {
    await page.goto('/clusters')
    const rows = page.getByRole('table', { name: 'Clusters' }).getByRole('row')
    await expect(rows).toHaveCount(7)
    await page.getByRole('button', { name: /^Scope:/ }).click()
    await page.getByRole('combobox', { name: 'Account filter' }).selectOption({ label: 'staging-example' })
    await expect(rows).toHaveCount(2)
  })
})
