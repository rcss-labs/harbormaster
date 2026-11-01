import { asUser, expect, test } from './fixtures/test'
import { SVC } from './fixtures/ids'

// Read-only: runs in the observe project against the unmutated seed.
const main = (page: import('@playwright/test').Page) => page.getByRole('main')

/** The cluster squares in the Clusters tile: one link per cluster, named by its cluster. */
const clusterSquares = (page: import('@playwright/test').Page) =>
  main(page).getByText('Clusters', { exact: true }).locator('..').getByRole('link')

test.describe('overview as pavel', () => {
  test.use(asUser('pavel'))

  test('tiles, needs attention and the poll age', async ({ page }) => {
    await page.goto('/')
    await expect(page.getByRole('heading', { level: 1, name: 'Overview' })).toBeVisible()
    await expect(clusterSquares(page)).toHaveCount(6)

    const attention = main(page).getByText('Needs attention', { exact: true }).locator('../..')
    // image-resizer also raises a warning row; the danger one carries the Failing pill and the OOM exit code.
    const resizer = attention.getByTestId('alert-row').filter({ hasText: 'Failing' }).filter({ hasText: SVC.imageResizer[3] })
    await expect(resizer).toContainText('137')
    await expect(attention.getByTestId('alert-row').filter({ hasText: SVC.catalogApi[3] }).first()).toBeVisible()

    // The product renders "12s ago" (no space), not the "12 s ago" of the plan; "N min ago" covers a loaded machine.
    await expect(main(page).getByText(/last poll (\d+s|\d+ min) ago/)).toBeVisible()
  })

  test('the deployments chart has no Invalid Date label', async ({ page }) => {
    await page.goto('/')
    await expect(main(page).getByText('Deployments per day, 14 days')).toBeVisible()
    await expect(main(page).getByText('today')).toBeVisible()
    expect(await main(page).innerText()).not.toContain('Invalid Date')
    // Tooltips live in SVG <title> elements, which innerText skips.
    expect(await main(page).textContent()).not.toContain('Invalid Date')
  })
})

test.describe('overview as omer', () => {
  test.use(asUser('omer'))

  test('sees five clusters, no Prod data and only his accounts', async ({ page }) => {
    await page.goto('/')
    await expect(page.getByRole('heading', { level: 1, name: 'Overview' })).toBeVisible()
    await expect(clusterSquares(page)).toHaveCount(5)
    await expect(clusterSquares(page).filter({ has: page.locator('xpath=self::*[@title="core-services"]') })).toHaveCount(0)
    // Wait for the data-driven sections so the absence checks below are not vacuous.
    await expect(main(page).getByText('Needs attention', { exact: true })).toBeVisible()
    await expect(main(page).getByText(/Live events/)).toBeVisible()
    await expect(main(page).getByText(SVC.imageResizer[3]).first()).toBeVisible()
    expect(await page.locator('body').textContent()).not.toContain('payments-api')

    const options = await page.getByLabel('Account filter').locator('option').allTextContents()
    expect(options.filter((name) => name !== 'All accounts').sort()).toEqual(['homelab-admin', 'staging-example'])
  })
})
