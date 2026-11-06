import { expectPhoneLayoutClean, watchPage } from './fixtures/phone'
import { asUser, expect, test } from './fixtures/test'

// Read-only phone checks for Analytics. The viewport is set here so the spec is a phone spec whichever project picks it up.
test.use({ ...asUser('sam'), viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true, deviceScaleFactor: 3 })

const TABS = ['delivery', 'reliability', 'capacity', 'operations'] as const

for (const tab of TABS) {
  test(`${tab} tab renders clean at 390 px`, async ({ page }) => {
    watchPage(page)
    await page.goto(`/analytics/${tab}`)
    await expect(page.getByRole('main')).toBeVisible()
    await page.waitForLoadState('networkidle')
    await expect(page.getByRole('heading', { level: 1, name: 'Analytics' })).toBeAttached()
    await expectPhoneLayoutClean(page)
  })
}

test('the tag chips filter the tab and All clears the tag', async ({ page }) => {
  watchPage(page)
  await page.goto('/analytics/delivery')
  const chips = page.getByRole('group', { name: 'Tag' })
  await expect(chips.getByRole('button', { name: 'All' })).toHaveAttribute('aria-pressed', 'true')
  await chips.getByRole('button', { name: 'Prod' }).click()
  await expect(page).toHaveURL(/tag=3/)
  await expect(chips.getByRole('button', { name: 'Prod' })).toHaveAttribute('aria-pressed', 'true')
  await expectPhoneLayoutClean(page)
  await chips.getByRole('button', { name: 'All' }).click()
  await expect(page).not.toHaveURL(/tag=/)
})

test('capacity keeps the range filter disabled and explains a tap with the toast', async ({ page }) => {
  watchPage(page)
  await page.goto('/analytics/capacity?range=24h')
  const range = page.getByRole('radiogroup', { name: 'Range' })
  await expect(range.getByRole('radio', { name: '90d' })).toHaveAttribute('aria-disabled', 'true')
  await expectPhoneLayoutClean(page)
  await range.getByRole('radio', { name: '90d' }).click({ force: true }) // aria-disabled, not disabled: the tap must still reach the toast
  await expect(page.getByText('This page does not use the range filter')).toBeVisible()
  await expect(page).toHaveURL(/range=24h/)
})
