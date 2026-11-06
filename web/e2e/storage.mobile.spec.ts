import type { Page } from '@playwright/test'
import { expectPhoneLayoutClean, expectSheetWithinViewport, watchPage } from './fixtures/phone'
import { asUser, expect, test } from './fixtures/test'

// Read-only phone checks for Storage. The viewport is set here so the spec is a phone spec whichever project picks it up.
test.use({ ...asUser('sam'), viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true, deviceScaleFactor: 3 })

// The storage tier's first pass runs a few seconds after the server starts.
const POLL_TIMEOUT = 30_000

const fsRows = (page: Page) => page.getByRole('table', { name: 'File systems' }).locator('[data-hm-row]')

test('file systems render as rows at 390 px and the layout is clean', async ({ page }) => {
  watchPage(page)
  await page.goto('/storage')
  await expect(page.getByRole('main')).toBeVisible()
  await expect(fsRows(page).filter({ hasText: 'uploads-archive' })).toBeVisible({ timeout: POLL_TIMEOUT })
  await expect(fsRows(page)).toHaveCount(5)
  await expectPhoneLayoutClean(page)
})

test('Not mounted is shown on its row and the details sheet names the reason', async ({ page }) => {
  watchPage(page)
  await page.goto('/storage?seg=unmounted')
  const archive = fsRows(page).filter({ hasText: 'uploads-archive' })
  await expect(archive).toBeVisible({ timeout: POLL_TIMEOUT })
  await expect(archive.getByText('Not mounted')).toBeVisible()
  await page.getByRole('button', { name: 'Details for uploads-archive' }).click()
  const sheet = page.getByRole('dialog', { name: 'uploads-archive' })
  await expect(sheet.getByText('No service mounts this file system.')).toBeVisible()
  await expectSheetWithinViewport(page)
  await page.keyboard.press('Escape')
  await expect(sheet).toBeHidden()
  await expectPhoneLayoutClean(page)
})

test('the sort and fields sheet opens within the screen and closes with Done', async ({ page }) => {
  watchPage(page)
  await page.goto('/storage')
  await expect(fsRows(page).filter({ hasText: 'uploads-archive' })).toBeVisible({ timeout: POLL_TIMEOUT })
  await page.getByRole('button', { name: 'Sort and fields' }).click()
  const sheet = page.getByRole('dialog', { name: 'Sort and fields' })
  await expect(sheet).toBeVisible()
  await expectSheetWithinViewport(page)
  await sheet.getByRole('button', { name: 'Done' }).click()
  await expect(sheet).toBeHidden()
  await expectPhoneLayoutClean(page)
})

test.describe('storage as omer', () => {
  test.use(asUser('omer'))

  test('omer sees four file systems at 390 px and the archive is absent', async ({ page }) => {
    watchPage(page)
    await page.goto('/storage')
    await expect(fsRows(page).filter({ hasText: 'reports-data' })).toBeVisible({ timeout: POLL_TIMEOUT })
    await expect(fsRows(page)).toHaveCount(4)
    await expect(fsRows(page).filter({ hasText: 'uploads-archive' })).toHaveCount(0)
    await expectPhoneLayoutClean(page)
  })
})
