import { asUser, expect, test } from './fixtures/test'
import type { Page } from '@playwright/test'

// Read-only: runs in the observe project. Tag ids on a fresh DB: Dev 1, Staging 2, Prod 3.
// Always target the top bar control by its exact name: the Clusters filter bar has a second control labelled "Tags".
const tagButton = (page: Page) => page.getByRole('button', { name: 'Tag filter', exact: true })
const tagOptions = (page: Page) => page.getByRole('group', { name: 'Tag filter options' })
const main = (page: Page) => page.getByRole('main')
const nav = (page: Page) => page.getByRole('navigation', { name: 'Main' })

async function toggleTag(page: Page, name: string) {
  await tagButton(page).click()
  await tagOptions(page).getByRole('checkbox', { name }).click()
  await page.keyboard.press('Escape')
}

/** Header row plus one row per listed cluster. */
const clusterRows = (page: Page) => page.getByRole('table', { name: 'Clusters' }).getByRole('row')

test.describe('tag filter as pavel', () => {
  test.use(asUser('pavel'))

  test('Prod shows core-services and the overridden artifact-proxy in ci-tools', async ({ page }) => {
    await page.goto('/clusters')
    await toggleTag(page, 'Prod')
    await expect(page).toHaveURL(/tag=3(&|$)/)
    await expect(clusterRows(page)).toHaveCount(3)
    const ciTools = page.getByRole('row', { name: /ci-tools/ })
    await expect(ciTools).toContainText('artifact-proxy')
    await expect(ciTools).not.toContainText('jenkins-controller')
    await expect(nav(page).getByRole('link', { name: /^Clusters/ })).toHaveText(/Clusters2$/)
  })

  test('Prod + Staging is OR', async ({ page }) => {
    await page.goto('/clusters')
    await toggleTag(page, 'Prod')
    await toggleTag(page, 'Staging')
    await expect(clusterRows(page)).toHaveCount(4)
    // Tag ids are kept sorted ascending in the URL (controller ruling), not in click order.
    await expect(page).toHaveURL(/tag=2&tag=3/)
  })

  test('Dev matches untagged clusters and non-overridden services', async ({ page }) => {
    await page.goto('/clusters')
    await toggleTag(page, 'Dev')
    await expect(clusterRows(page)).toHaveCount(5)
    const ciTools = page.getByRole('row', { name: /ci-tools/ })
    await expect(ciTools).toContainText('jenkins-controller')
    await expect(ciTools).not.toContainText('artifact-proxy')
  })

  test('the filter follows to Overview, Tasks and Needs attention', async ({ page }) => {
    await page.goto('/clusters')
    await toggleTag(page, 'Dev')
    await expect(clusterRows(page)).toHaveCount(5)

    await nav(page).getByRole('link', { name: /^Overview/ }).click()
    await expect(page.getByRole('heading', { level: 1, name: 'Overview' })).toBeVisible()
    // Tile reads its number straight from the DOM: "Clusters" label, then the count.
    const clustersTile = main(page).getByText('Clusters', { exact: true }).locator('..')
    await expect(clustersTile).toContainText(/^Clusters4\d+ healthy$/)
    const resizerAlerts = main(page).getByTestId('alert-row').filter({ hasText: 'image-resizer' })
    await expect(resizerAlerts.first()).toBeVisible({ timeout: 30_000 })

    // Switch Dev -> Prod.
    await toggleTag(page, 'Dev')
    await toggleTag(page, 'Prod')
    await expect(clustersTile).toContainText(/^Clusters2\d+ healthy$/)
    await expect(main(page).getByText('Needs attention', { exact: true })).toBeVisible()
    await expect(resizerAlerts).toHaveCount(0)

    await page.goto('/tasks?filter=running')
    await expect(page).toHaveURL(/tag=/)
    // Wait for a Prod row first, so the absence check cannot pass on a table that has not rendered.
    await expect(page.getByRole('row').filter({ hasText: 'core-services' }).first()).toBeVisible()
    await expect(page.getByRole('row').filter({ hasText: 'release-build' })).toHaveCount(0)
  })

  test('it is remembered', async ({ page }) => {
    await page.goto('/clusters')
    await toggleTag(page, 'Prod')
    await expect(page).toHaveURL(/tag=3/)
    // localStorage lasts inside this test only, so set and reload in the same test.
    await page.goto('/clusters')
    await expect(page).toHaveURL(/tag=3/)
    await expect(tagButton(page)).toHaveText('Prod')
  })
})

test.describe('tag filter as omer', () => {
  test.use(asUser('omer'))

  test('cannot widen to Prod', async ({ page }) => {
    await page.goto('/clusters?tag=3')
    await expect(page.getByRole('row', { name: /core-services/ })).toHaveCount(0)
    await expect(page.getByText(/No clusters match/)).toBeVisible()
    await tagButton(page).click()
    await expect(tagOptions(page).getByRole('checkbox', { name: 'Dev' })).toHaveCount(1)
    await expect(tagOptions(page).getByRole('checkbox', { name: 'Prod' })).toHaveCount(0)
  })
})
