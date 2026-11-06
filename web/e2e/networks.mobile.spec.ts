import { expectPhoneLayoutClean, expectSheetWithinViewport, watchPage } from './fixtures/phone'
import { asUser, expect, test } from './fixtures/test'

// Read-only phone checks for Networks. The viewport is set here so the spec is a phone spec whichever project picks it up.
test.use({ ...asUser('sam'), viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true, deviceScaleFactor: 3 })

// The network tier's first pass runs a few seconds after the server starts.
const POLL_TIMEOUT = 30_000
const HOMELAB_ACCOUNT_ID = '482109375512'
const MAIN_VPC = 'vpc-0a41c7e29b3d'

test('the VPC list is clean at 390 px and opens no sheet on load', async ({ page }) => {
  watchPage(page)
  await page.goto('/networks')
  await expect(page.getByRole('region', { name: 'VPCs' }).getByRole('button', { name: /homelab-main/ })).toBeVisible({ timeout: POLL_TIMEOUT })
  await expect(page.getByRole('dialog')).toHaveCount(0)
  await expectPhoneLayoutClean(page)
})

test('a VPC opens its subnets and placement in a sheet that stays within the screen', async ({ page }) => {
  watchPage(page)
  await page.goto('/networks')
  await page.getByRole('region', { name: 'VPCs' }).getByRole('button', { name: /homelab-main/ }).click({ timeout: POLL_TIMEOUT })
  const sheet = page.getByRole('dialog', { name: 'homelab-main' })
  await expect(sheet).toBeVisible()
  await expect(sheet.getByRole('table', { name: 'Subnets' })).toBeVisible()
  await expectSheetWithinViewport(page)
  await expectPhoneLayoutClean(page)
  await sheet.getByRole('button', { name: 'Close panel' }).click()
  await expect(sheet).toBeHidden()
  await expect(page.getByRole('region', { name: 'VPCs' })).toBeVisible()
})

test('a deep link opens the VPC sheet at 390 px', async ({ page }) => {
  watchPage(page)
  await page.goto(`/networks?vpc=${encodeURIComponent(`${HOMELAB_ACCOUNT_ID}/us-east-1/${MAIN_VPC}`)}`)
  await expect(page.getByRole('dialog', { name: 'homelab-main' })).toBeVisible({ timeout: POLL_TIMEOUT })
  await expectSheetWithinViewport(page)
  await expectPhoneLayoutClean(page)
})
