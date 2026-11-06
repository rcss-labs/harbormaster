import { asUser, expect, test } from './fixtures/test'
import { SVC } from './fixtures/ids'

// Read-only: runs in the observe project against the unmutated seed.
const main = (page: import('@playwright/test').Page) => page.getByRole('main')

/** The cells of the Estate map: one link per cluster, named "cluster, status, tasks, region". */
const clusterSquares = (page: import('@playwright/test').Page) =>
  main(page).getByRole('region', { name: 'Estate map' }).getByRole('link')

test.describe('overview as pavel', () => {
  test.use(asUser('pavel'))

  test('tiles, needs attention and the poll age', async ({ page }) => {
    await page.goto('/')
    await expect(page.getByRole('heading', { level: 1, name: 'Overview' })).toBeVisible()
    await expect(clusterSquares(page)).toHaveCount(6)

    const attention = main(page).getByRole('region', { name: 'Needs attention' })
    // image-resizer also raises a warning row; the danger one carries the Failing pill and the OOM exit code.
    const resizer = attention.getByTestId('alert-row').filter({ hasText: 'Failing' }).filter({ hasText: SVC.imageResizer[3] })
    await expect(resizer).toContainText('137')
    await expect(attention.getByTestId('alert-row').filter({ hasText: SVC.catalogApi[3] }).first()).toBeVisible()

    // The product renders "12s ago" (no space), not the "12 s ago" of the plan; "N min ago" covers a loaded machine.
    await expect(main(page).getByText(/last poll (\d+s|\d+ min) ago/)).toBeVisible()
  })

  test('the layout toggle switches to the Estate list and is remembered', async ({ page }) => {
    await page.goto('/')
    await expect(main(page).getByRole('region', { name: 'Needs attention' })).toBeVisible()
    await main(page).getByRole('radio', { name: 'Estate' }).click()
    await expect(main(page).getByRole('region', { name: 'Services' })).toBeVisible()
    await page.reload()
    await expect(main(page).getByRole('region', { name: 'Services' })).toBeVisible()
    await main(page).getByRole('radio', { name: 'Triage' }).click()
    await expect(main(page).getByRole('region', { name: 'Needs attention' })).toBeVisible()
  })
})

test.describe('overview at 390 px', () => {
  test.use({ ...asUser('pavel'), viewport: { width: 390, height: 844 } })

  test('neither layout scrolls sideways', async ({ page }) => {
    // <main> is the scroll container, so sideways overflow shows up on it.
    const overflow = () => main(page).evaluate((el) => el.scrollWidth - el.clientWidth)
    await page.goto('/')
    await expect(main(page).getByRole('region', { name: 'Needs attention' }).getByTestId('alert-row').first()).toBeVisible()
    await expect(clusterSquares(page)).toHaveCount(6)
    expect(await overflow()).toBeLessThanOrEqual(0)
    await main(page).getByRole('radio', { name: 'Estate' }).click()
    await expect(main(page).getByRole('region', { name: 'Services' })).toBeVisible()
    expect(await overflow()).toBeLessThanOrEqual(0)
  })
})

test.describe('overview as omer', () => {
  test.use(asUser('omer'))

  test('sees five clusters, no Prod data and only his accounts', async ({ page }) => {
    await page.goto('/')
    await expect(page.getByRole('heading', { level: 1, name: 'Overview' })).toBeVisible()
    await expect(clusterSquares(page)).toHaveCount(5)
    await expect(main(page).getByRole('region', { name: 'Estate map' }).getByRole('link', { name: /^core-services,/ })).toHaveCount(0)
    // Wait for the data-driven sections so the absence checks below are not vacuous.
    await expect(main(page).getByRole('region', { name: 'Needs attention' })).toBeVisible()
    await expect(main(page).getByText(SVC.imageResizer[3]).first()).toBeVisible()
    expect(await page.locator('body').textContent()).not.toContain('payments-api')

    await page.getByRole('button', { name: /^Scope:/ }).click()
    const options = await page.getByLabel('Account filter').locator('option').allTextContents()
    expect(options.filter((name) => name !== 'All accounts').sort()).toEqual(['homelab-admin', 'staging-example'])
  })
})
