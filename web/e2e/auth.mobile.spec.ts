import { expectPhoneLayoutClean, watchPage } from './fixtures/phone'
import { asUser, expect, test } from './fixtures/test'

const PHONE = { viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true, deviceScaleFactor: 3 } as const

test.describe('sign-in at 390', () => {
  test.use({ storageState: { cookies: [], origins: [] }, ...PHONE })

  test('the login form is full width, clean, and shows the wrong-password text', async ({ page }) => {
    // The wrong-password attempt is a 401 on the login call; everything else must stay quiet.
    watchPage(page, (method, url, status) => status === 401 && (url.endsWith('/api/me') || (method === 'POST' && url.endsWith('/api/auth/login'))))
    await page.goto('/login')
    const username = page.getByLabel('Username')
    await expect(username).toBeVisible()
    await page.waitForLoadState('networkidle')
    await expectPhoneLayoutClean(page)
    const box = await username.evaluate((el) => ({ width: el.getBoundingClientRect().width, view: document.documentElement.clientWidth }))
    expect(box.width).toBeGreaterThan(box.view - 48)

    await username.fill('sam')
    await page.getByLabel('Password').fill('definitely-wrong')
    await page.getByRole('button', { name: 'Sign in' }).click()
    await expect(page.getByRole('alert')).toHaveText('Wrong username or password.')
    await expectPhoneLayoutClean(page)
  })
})

test.describe('change password at 390', () => {
  test.use({ ...asUser('sam'), ...PHONE })

  test('the password page is clean and keeps its fields', async ({ page }) => {
    watchPage(page)
    await page.goto('/password')
    await expect(page.getByLabel('Current password')).toBeVisible()
    await expect(page.getByLabel('New password')).toBeVisible()
    await expect(page.getByLabel('Confirm')).toBeVisible()
    await expect(page.getByRole('button', { name: 'Change password' })).toBeVisible()
    await page.waitForLoadState('networkidle')
    await expectPhoneLayoutClean(page)
  })
})
