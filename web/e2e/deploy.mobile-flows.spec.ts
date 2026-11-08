import { asUser, expect, test } from './fixtures/test'
import { expectPhoneLayoutClean, expectSheetWithinViewport, watchPage } from './fixtures/phone'
import { SVC, serviceUrl } from './fixtures/ids'

// Applies a change from a phone, so it runs after the flow specs and assumes nothing about the seed's revision numbers.
// A long value in a text field scrolls inside the field, which is how inputs behave.
const LONG_VALUES_SCROLL = { allowClip: ['input'] }

test.use({ ...asUser('sam'), viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true, deviceScaleFactor: 3 })


test('change an env var, review it in the sheet and apply', async ({ page }) => {
  watchPage(page)
  await page.goto(serviceUrl(...SVC.paymentsApi, 'deploy'))
  await expect(page.getByRole('button', { name: 'Add variable' })).toBeVisible()
  await page.getByRole('button', { name: 'Add variable' }).click()
  await page.getByLabel(/^Variable name \d+$/).last().fill('PHONE_FLOW_CHECK')
  await page.getByLabel(/^Variable value \d+$/).last().fill('on')

  const bar = page.getByRole('region', { name: 'Pending changes' })
  await expect(bar.getByText('1 change')).toBeVisible()
  await expectPhoneLayoutClean(page, LONG_VALUES_SCROLL)

  await bar.getByRole('button', { name: 'Review…' }).click()
  const review = page.getByRole('dialog', { name: 'Review changes to payments-api' })
  await expect(review).toBeVisible()
  await expectSheetWithinViewport(page)
  await review.getByRole('textbox', { name: 'Reason' }).fill('phone flow check')
  await review.getByRole('button', { name: /^(Register and deploy :\d+|Apply)/ }).click()

  await expect(page.getByText(/^(Deploying payments-api to :\d+\.|Applied the changes to payments-api\.)$/)).toBeVisible()
  await expect(bar).toHaveCount(0)
})
