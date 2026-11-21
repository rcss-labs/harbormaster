import { deleteE2eRoutes, openNotifications, startRoute } from './notifications.helpers'
import { asUser, expect, test } from './fixtures/test'

// Phone width: add and test a route in the editor sheet, and the page never scrolls sideways. The route is deleted again.
test.use({ ...asUser('sam'), viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true, deviceScaleFactor: 3 })
test.afterEach(async ({ request }) => deleteE2eRoutes(request))

const overflow = (page: import('@playwright/test').Page) =>
  page.evaluate(() => {
    const main = document.querySelector('main')
    return { document: document.documentElement.scrollWidth - window.innerWidth, main: main ? main.scrollWidth - main.clientWidth : 0 }
  })

test('adds and tests a route in the sheet without scrolling the page sideways', async ({ page, baseURL }) => {
  await openNotifications(page)
  await page.waitForLoadState('networkidle')
  expect((await overflow(page)).document).toBeLessThanOrEqual(0)

  await startRoute(page, 'e2e phone hook', 'Webhook')
  const sheet = page.getByRole('dialog', { name: 'Route editor' })
  await expect(sheet).toBeVisible()
  await sheet.getByLabel('Webhook URL').fill(`${baseURL}/fake-notify/webhook/ok`)
  await sheet.getByRole('button', { name: 'Save route' }).click()
  await expect(page.getByText('Saved "e2e phone hook".')).toBeVisible()
  await sheet.getByRole('button', { name: 'Send test' }).click()
  await expect(page.getByText(/^Test sent to .*See the delivery log\.$/)).toBeVisible()
  expect((await overflow(page)).document).toBeLessThanOrEqual(0)
  expect((await overflow(page)).main).toBeLessThanOrEqual(0)

  await page.getByRole('button', { name: 'Close' }).click()
  await page.getByRole('button', { name: 'Delivery log', exact: true }).click()
  await expect(page.getByRole('list', { name: 'Deliveries' })).toBeVisible()
  expect((await overflow(page)).document).toBeLessThanOrEqual(0)
})
