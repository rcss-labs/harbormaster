import { asUser, authFile, expect, newGuardedContext, test } from './fixtures/test'
import { USERS } from './fixtures/users'

// Read-only: runs in the observe project.

test.describe('my access as omer', () => {
  test.use(asUser('omer'))

  test('shows his own permissions per tag, read-only', async ({ page }) => {
    await page.goto('/me/access')
    await expect(page.getByRole('heading', { level: 1, name: 'My access' })).toBeVisible()
    await expect(page.getByText('What you can do on each tag. Ask a global admin to change it.')).toBeVisible()

    const row = (tag: string) => page.getByRole('row').filter({ has: page.getByText(tag, { exact: true }) })
    // Columns: Tag, View, Logs, Operate, Deploy, Edit task definition, Terminal, Services.
    const marks = async (tag: string) => (await row(tag).getByRole('cell').allTextContents()).slice(1, 7)
    await expect(row('Dev')).toBeVisible()
    expect(await marks('Dev')).toEqual(['✓', '✓', '✓', '–', '–', '–'])
    expect(await marks('Staging')).toEqual(['✓', '–', '–', '–', '–', '–'])
    expect(await marks('Prod')).toEqual(['–', '–', '–', '–', '–', '–'])

    // One data row per tag and nothing editable.
    await expect(page.getByRole('table').getByRole('combobox')).toHaveCount(0)
    await expect(page.getByRole('row').filter({ hasText: 'omer' })).toHaveCount(0)
  })
})

// A server-side sign-out ends the session. Signing dana out through the shared storageState would break every later
// spec that reuses .auth/dana.json, so this test signs in again in a context of its own.
test('sign-out as dana ends the session and returns to the login page', async ({ browser, playwright, baseURL }) => {
  const { context, assertClean } = await newGuardedContext(browser, { baseURL: baseURL, storageState: { cookies: [], origins: [] } })
  const page = await context.newPage()
  await page.goto('/login')
  await page.getByLabel('Username').fill(USERS.dana.username)
  await page.getByLabel('Password').fill(USERS.dana.password)
  await page.getByRole('button', { name: 'Sign in' }).click()
  await expect(page.getByRole('navigation', { name: 'Main' })).toBeVisible()

  await expect(page.getByText('Signed in as')).toContainText('dana')
  await expect(page.getByText('Signed in as')).toContainText('User')
  await page.getByRole('button', { name: 'Sign out' }).click()
  await expect(page).toHaveURL(/\/login$/)
  expect((await page.request.get('/api/me')).status()).toBe(401)

  // The shared dana session from setup is a different session and stays valid.
  const shared = await playwright.request.newContext({ baseURL: baseURL, storageState: authFile('dana') })
  expect((await shared.get('/api/me')).status()).toBe(200)
  await shared.dispose()
  await assertClean()
  await context.close()
})
