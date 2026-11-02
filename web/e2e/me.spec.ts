import { asUser, authFile, expect, newGuardedContext, test } from './fixtures/test'
import { USERS } from './fixtures/users'

// Read-only: runs in the observe project.

test.describe('my access as omer', () => {
  test.use(asUser('omer'))

  test('shows his own permissions per tag, read-only', async ({ page }) => {
    await page.goto('/me/access')
    await expect(page.getByRole('heading', { level: 1, name: 'My access' })).toBeVisible()
    await expect(page.getByText('What you can do on each tag. Ask a global admin to change it.')).toBeVisible()

    const table = page.getByRole('table')
    const row = (tag: string) => table.getByRole('row').filter({ has: page.getByText(tag, { exact: true }) })
    // Columns: Tag, View, Logs, Operate, Deploy, Edit task definition, Terminal, Services. Each read retries.
    const expectMarks = async (tag: string, marks: string[]) => {
      const cells = row(tag).getByRole('cell')
      for (const [index, mark] of marks.entries()) await expect(cells.nth(index + 1)).toHaveText(mark)
    }
    await expectMarks('Dev', ['✓', '✓', '✓', '–', '–', '–'])
    await expectMarks('Staging', ['✓', '–', '–', '–', '–', '–'])
    await expectMarks('Prod', ['–', '–', '–', '–', '–', '–'])

    // Exactly one data row per tag (plus the header row), none for other users, and nothing editable.
    await expect(table.getByRole('row')).toHaveCount(4)
    await expect(table.getByRole('row').filter({ hasText: /pavel|dana|oncall|omer/ })).toHaveCount(0)
    await expect(table.getByRole('combobox')).toHaveCount(0)
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
  await expect(page).toHaveURL(/\/login(\?signed_out=1)?$/)
  expect((await page.request.get('/api/me')).status()).toBe(401)

  // The shared dana session from setup is a different session and stays valid.
  const shared = await playwright.request.newContext({ baseURL: baseURL, storageState: authFile('dana') })
  expect((await shared.get('/api/me')).status()).toBe(200)
  await shared.dispose()
  await assertClean()
  await context.close()
})
