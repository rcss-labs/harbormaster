import { asUser, expect, test } from './fixtures/test'
import { SVC } from './fixtures/ids'

// Read-only: runs in the observe project against the unmutated seed.
const main = (page: import('@playwright/test').Page) => page.getByRole('main')

/** The cells of the Estate map: one link per cluster, named "cluster, status, tasks, region". */
const clusterSquares = (page: import('@playwright/test').Page) => main(page).getByRole('region', { name: 'Estate map' }).getByRole('link')

test.describe('overview as sam', () => {
  test.use(asUser('sam'))

  test('tiles, needs attention and the poll age', async ({ page }) => {
    await page.goto('/')
    await expect(page.getByRole('heading', { level: 1, name: 'Overview' })).toBeVisible()
    await expect(clusterSquares(page)).toHaveCount(6)

    const attention = main(page).getByRole('region', {
      name: 'Needs attention',
    })
    // image-resizer also raises a warning row; the danger one carries the Failing pill and the OOM exit code.
    const resizer = attention.getByTestId('alert-row').filter({ hasText: 'Failing' }).filter({ hasText: SVC.imageResizer[3] })
    await expect(resizer).toContainText('137')
    await expect(attention.getByTestId('alert-row').filter({ hasText: SVC.catalogApi[3] }).first()).toBeVisible()

    // The product renders "12s ago" (no space), not the "12 s ago" of the plan; "N min ago" covers a loaded machine.
    await expect(main(page).getByText(/last poll (\d+s|\d+ min) ago/)).toBeVisible()
  })

  test('the Services tile, the Needs attention count and the Problems view agree', async ({ page }) => {
    await page.goto('/')
    const services = (await (await page.request.get('/api/services')).json()) as { items: { status: string; attention: string }[] }
    const expected = services.items.filter(
      (item) => item.attention !== '' || ['failing', 'degraded', 'unhealthy_task', 'deploying'].includes(item.status),
    ).length
    expect(expected).toBeGreaterThan(0)
    const tile = main(page).getByRole('list', { name: 'Estate key figures' }).getByRole('listitem').filter({ hasText: 'Services' })
    await expect(tile).toContainText(`${expected} need attention`)
    await expect(main(page).getByRole('region', { name: 'Needs attention' })).toContainText(
      new RegExp(`${expected} ${expected === 1 ? 'service' : 'services'}`),
    )
    await tile.getByRole('link').click()
    await expect(page.getByRole('group', { name: 'Saved views' }).getByRole('button', { name: `Problems ${expected}` })).toBeVisible()
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
  test.use({ ...asUser('sam'), viewport: { width: 390, height: 844 } })

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

test.describe('needs attention at 390 px', () => {
  test.use({ ...asUser('sam'), viewport: { width: 390, height: 844 } })

  test('every severity chip is fully inside the window and the page does not scroll sideways', async ({ page }) => {
    await page.goto('/')
    const attention = main(page).getByRole('region', {
      name: 'Needs attention',
    })
    await expect(attention.getByTestId('alert-row').first()).toBeVisible()
    for (const name of [/^All/, /^Failing/, /^Warning/, /^In progress/]) {
      const chip = attention.getByRole('radio', { name })
      await expect(chip).toBeVisible()
      const box = await chip.boundingBox()
      expect(box).not.toBeNull()
      expect(box!.x).toBeGreaterThanOrEqual(0)
      expect(box!.x + box!.width).toBeLessThanOrEqual(390)
    }
    expect(await main(page).evaluate((el) => el.scrollWidth - el.clientWidth)).toBeLessThanOrEqual(0)
    expect(await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)).toBeLessThanOrEqual(0)
  })
})

test.describe('overview as omer', () => {
  test.use(asUser('omer'))

  test('sees five clusters, no Prod data and only his accounts', async ({ page }) => {
    await page.goto('/')
    await expect(page.getByRole('heading', { level: 1, name: 'Overview' })).toBeVisible()
    await expect(clusterSquares(page)).toHaveCount(5)
    await expect(
      main(page)
        .getByRole('region', { name: 'Estate map' })
        .getByRole('link', { name: /^core-services,/ }),
    ).toHaveCount(0)
    // Wait for the data-driven sections so the absence checks below are not vacuous.
    await expect(main(page).getByRole('region', { name: 'Needs attention' })).toBeVisible()
    await expect(main(page).getByText(SVC.imageResizer[3]).first()).toBeVisible()
    expect(await page.locator('body').textContent()).not.toContain('payments-api')

    await page.getByRole('button', { name: /^Scope:/ }).click()
    const options = await page.getByLabel('Account filter').locator('option').allTextContents()
    expect(options.filter((name) => name !== 'All accounts').sort()).toEqual(['homelab-admin', 'staging-example'])
  })
})
