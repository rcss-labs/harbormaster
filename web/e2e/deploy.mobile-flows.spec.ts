import { asUser, expect, test } from './fixtures/test'
import { expectPhoneLayoutClean, expectSheetWithinViewport, watchPage } from './fixtures/phone'
import { serviceUrl } from './fixtures/ids'

// Applies a change from a phone, so it runs after the flow specs and assumes nothing about the seed's revision numbers.
test.use({ ...asUser('sam'), viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true, deviceScaleFactor: 3 })

// A Dev service no other spec depends on after the flows have run.
const searchIndexer = ['482109375512', 'eu-west-1', 'eu-apps', 'search-indexer'] as const

test('change an env var, review it in the sheet and apply', async ({ page }) => {
  watchPage(page)
  await page.goto(serviceUrl(...searchIndexer, 'deploy'))
  await expect(page.getByRole('button', { name: 'Add variable' })).toBeVisible()
  await page.getByRole('button', { name: 'Add variable' }).click()
  await page.getByLabel(/^Variable name \d+$/).last().fill('PHONE_FLOW_CHECK')
  await page.getByLabel(/^Variable value \d+$/).last().fill('on')

  const bar = page.getByRole('region', { name: 'Pending changes' })
  await expect(bar.getByText('1 change')).toBeVisible()
  await expectPhoneLayoutClean(page)

  await bar.getByRole('button', { name: 'Review…' }).click()
  const review = page.getByRole('dialog', { name: 'Review changes to search-indexer' })
  await expect(review).toBeVisible()
  await expectSheetWithinViewport(page)
  await review.getByRole('textbox', { name: 'Reason' }).fill('phone flow check')
  await review.getByRole('button', { name: /^(Register and deploy :\d+|Apply)/ }).click()

  await expect(page.getByText(/^(Deploying search-indexer to :\d+\.|Applied the changes to search-indexer\.)$/)).toBeVisible()
  await expect(bar).toHaveCount(0)
})
