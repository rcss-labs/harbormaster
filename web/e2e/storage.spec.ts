import { expect, test, type Page } from '@playwright/test'
import { login, saveScreenshot } from './helpers'

// The storage tier's first pass runs a few seconds after the server starts.
const POLL_TIMEOUT = 30_000

// One EFS card per file system: a section named after it, holding an h3.
const fsCards = (page: Page) => page.getByRole('region').filter({ has: page.getByRole('heading', { level: 3 }) })

test('admin sees five file systems including Not mounted', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 900 })
  await login(page, 'pavel')
  await page.goto('/storage')

  const archive = page.getByRole('region', { name: 'uploads-archive' })
  await expect(archive).toBeVisible({ timeout: POLL_TIMEOUT })
  await expect(archive.getByText('Not mounted')).toBeVisible()
  await expect(fsCards(page)).toHaveCount(5)
  await expect(page.getByText('337.0 GiB in total')).toBeVisible()

  await saveScreenshot(page, 'storage.png')
})

test('dev user sees four', async ({ page }) => {
  await login(page, 'omer')
  await page.goto('/storage')
  await expect(fsCards(page).first()).toBeVisible({ timeout: POLL_TIMEOUT })
  await expect(fsCards(page)).toHaveCount(4)
  await expect(page.getByRole('region', { name: 'uploads-archive' })).toHaveCount(0)
})
