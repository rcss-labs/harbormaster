import { SVC, serviceUrl } from './fixtures/ids'
import { expectPhoneLayoutClean, watchPage } from './fixtures/phone'
import { asUser, expect, test } from './fixtures/test'

// Read-only phone check of the Timeline tab: the lanes scroll inside their panel, never the page.
test.use({ ...asUser('sam'), viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true, deviceScaleFactor: 3 })

const services = [
  { name: 'payments-api', url: serviceUrl(...SVC.paymentsApi, 'timeline') },
  { name: 'image-resizer', url: serviceUrl(...SVC.imageResizer, 'timeline') },
]

for (const { name, url } of services) {
  test(`${name} timeline is laid out cleanly at 390, also with an event selected`, async ({ page }) => {
    watchPage(page)
    await page.goto(url)
    await expect(page.getByRole('heading', { name, level: 1 })).toBeAttached()
    const marker = page.getByTestId('timeline-track').getByRole('button').first()
    await expect(marker).toBeVisible()
    await page.waitForLoadState('networkidle')
    await expectPhoneLayoutClean(page)

    await marker.click()
    await expect(page).toHaveURL(/event=/)
    await page.waitForLoadState('networkidle')
    await expectPhoneLayoutClean(page)
  })
}
