import { asUser, expect, test } from './fixtures/test'
import { expectPhoneLayoutClean, watchPage } from './fixtures/phone'

// Config search at phone width, reached through More. Read-only.
test.use({ ...asUser('sam'), viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true, deviceScaleFactor: 3 })

test('searches on a phone without horizontal scroll', async ({ page }) => {
  watchPage(page)
  await page.goto('/more')
  await page.getByRole('region', { name: 'Insights' }).getByRole('link', { name: 'Config search' }).tap()
  await expect(page).toHaveURL(/\/config$/)

  await page.getByRole('textbox', { name: 'Config search query' }).fill('env:NODE_ENV')
  await page.getByRole('form', { name: 'Config search' }).getByRole('button', { name: 'Search', exact: true }).tap()

  const cards = page.getByRole('list', { name: 'Matching services' })
  await expect(cards).toBeVisible()
  await expect(cards.getByRole('listitem')).toHaveCount(2)
  await expect(cards.getByRole('link', { name: 'payments-api' })).toBeVisible()
  expect(await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)).toBeLessThanOrEqual(0)
  await expectPhoneLayoutClean(page)
})
