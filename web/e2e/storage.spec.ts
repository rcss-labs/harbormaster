import { expect, test } from '@playwright/test'
import { login } from './helpers'

// The storage tier's first pass runs a few seconds after the server starts.
const POLL_TIMEOUT = 30_000

test('admin sees five file systems including Not mounted', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 900 })
  await login(page, 'pavel')
  await page.goto('/storage')

  const archive = page.getByRole('region', { name: 'uploads-archive' })
  await expect(archive).toBeVisible({ timeout: POLL_TIMEOUT })
  await expect(archive.getByText('Not mounted')).toBeVisible()
  await expect(page.getByRole('region').filter({ has: page.getByRole('heading', { level: 3 }) })).toHaveCount(5)
  await expect(page.getByText('337.0 GiB in total')).toBeVisible()

  const dir = process.env.HM_E2E_SCREENSHOT_DIR
  if (dir) await page.screenshot({ path: `${dir}/storage.png`, fullPage: true })
})

test('dev user sees four', async ({ page }) => {
  await login(page, 'omer')
  await page.goto('/storage')
  await expect(page.getByRole('heading', { level: 3 }).first()).toBeVisible({ timeout: POLL_TIMEOUT })
  await expect(page.getByRole('heading', { level: 3 })).toHaveCount(4)
  await expect(page.getByRole('heading', { level: 3, name: 'uploads-archive' })).toHaveCount(0)
})
