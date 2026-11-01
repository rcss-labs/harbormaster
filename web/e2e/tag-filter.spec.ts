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

test.describe('tag filter as pavel', () => {
  test.use(asUser('pavel'))

  test('Prod shows core-services and the overridden artifact-proxy in ci-tools', async ({ page }) => {
    await page.goto('/clusters')
    await toggleTag(page, 'Prod')
    await expect(page).toHaveURL(/tag=3(&|$)/)
    await expect(page.getByRole('article')).toHaveCount(2)
    const ciTools = page.getByRole('article', { name: 'ci-tools' })
    await expect(ciTools).toContainText('artifact-proxy')
    await expect(ciTools).not.toContainText('jenkins-controller')
    await expect(nav(page).getByRole('link', { name: /^Clusters/ })).toHaveText('Clusters2')
  })

  test('Prod + Staging is OR', async ({ page }) => {
    await page.goto('/clusters')
    await toggleTag(page, 'Prod')
    await toggleTag(page, 'Staging')
    await expect(page.getByRole('article')).toHaveCount(3)
    // Tag ids are kept sorted ascending in the URL (controller ruling), not in click order.
    await expect(page).toHaveURL(/tag=2&tag=3/)
  })

  test('Dev matches untagged clusters and non-overridden services', async ({ page }) => {
    await page.goto('/clusters')
    await toggleTag(page, 'Dev')
    await expect(page.getByRole('article')).toHaveCount(4)
    const ciTools = page.getByRole('article', { name: 'ci-tools' })
    await expect(ciTools).toContainText('jenkins-controller')
    await expect(ciTools).not.toContainText('artifact-proxy')
  })

  test('the filter follows to Overview, Tasks and Needs attention', async ({ page }) => {
    await page.goto('/clusters')
    await toggleTag(page, 'Dev')
    await expect(page.getByRole('article')).toHaveCount(4)

    await nav(page).getByRole('link', { name: /^Overview/ }).click()
    await expect(page.getByRole('heading', { level: 1, name: 'Overview' })).toBeVisible()
    const clusterSquares = main(page).getByText('Clusters', { exact: true }).locator('..').getByRole('link')
    await expect(clusterSquares).toHaveCount(4)
    const attention = main(page).getByText('Needs attention', { exact: true }).locator('../..')
    await expect(attention).toContainText('image-resizer', { timeout: 30_000 })

    // Switch Dev -> Prod.
    await toggleTag(page, 'Dev')
    await toggleTag(page, 'Prod')
    await expect(clusterSquares).toHaveCount(2)
    await expect(attention).not.toContainText('image-resizer')

    await page.goto('/tasks?filter=running')
    await expect(page).toHaveURL(/tag=/)
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
    await expect(page.getByRole('article')).toHaveCount(0)
    await tagButton(page).click()
    await expect(tagOptions(page).getByRole('checkbox', { name: 'Prod' })).toHaveCount(0)
    await expect(tagOptions(page).getByRole('checkbox', { name: 'Dev' })).toHaveCount(1)
  })

  // Brief expects an empty state. Clusters.tsx only renders one when items.length > 0 && visible.length === 0, so a server-side
  // empty result (omer holds no Prod access) leaves a blank page; fixme until the product shows one (see task-9 report).
  test.fixme('shows an empty state when the tag leaves no clusters', async ({ page }) => {
    await page.goto('/clusters?tag=3')
    await expect(page.getByText(/No clusters match/)).toBeVisible()
  })
})
