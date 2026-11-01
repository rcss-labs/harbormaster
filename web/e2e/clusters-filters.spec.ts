import { asUser, expect, test } from './fixtures/test'

// Read-only: runs in the observe project. Status, name and the shared account scope on the Clusters page.

test.describe('clusters filter bar as pavel', () => {
  test.use(asUser('pavel'))

  test('status, name and view', async ({ page }) => {
    await page.goto('/clusters')
    await expect(page.getByRole('article')).toHaveCount(6)

    await page.getByRole('group', { name: 'Status filter' }).getByRole('button', { name: 'Needs attention' }).click()
    await expect(page.getByRole('article')).toHaveCount(1)
    await expect(page.getByRole('article', { name: 'background-jobs' })).toBeVisible()
    await expect(page).toHaveURL(/status=needs_attention/)

    await page.getByRole('searchbox', { name: 'Cluster name' }).fill('core')
    await expect(page.getByRole('article')).toHaveCount(0)
    await expect(page.getByText('No clusters match these filters.')).toBeVisible()

    await page.getByRole('button', { name: 'Clear filters' }).first().click()
    await expect(page.getByRole('article')).toHaveCount(6)

    await page.getByRole('searchbox', { name: 'Cluster name' }).fill('eu')
    await expect(page.getByRole('article')).toHaveCount(1)
    await expect(page.getByRole('article', { name: 'eu-apps' })).toBeVisible()

    await page.getByRole('group', { name: 'View' }).getByRole('button', { name: 'Table' }).click()
    // Header row plus eu-apps.
    await expect(page.getByRole('row')).toHaveCount(2)
    await expect(page.getByRole('searchbox', { name: 'Cluster name' })).toHaveValue('eu')

    await page.goto('/clusters?status=healthy&q=web')
    await expect(page.getByRole('row')).toHaveCount(2)
    await expect(page.getByRole('row', { name: /staging-web/ })).toBeVisible()
  })

  test('account select in the bar drives the top bar', async ({ page }) => {
    await page.goto('/clusters')
    await expect(page.getByRole('article')).toHaveCount(6)
    await page.getByRole('combobox', { name: 'Account', exact: true }).selectOption({ label: 'staging-example' })
    await expect(page.getByRole('combobox', { name: 'Account filter' })).toHaveValue(
      await page.getByRole('combobox', { name: 'Account', exact: true }).inputValue(),
    )
    await expect(page.getByRole('article')).toHaveCount(1)
  })
})
