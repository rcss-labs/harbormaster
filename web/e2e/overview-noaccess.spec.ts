import { authFile, expect, newGuardedContext, test } from './fixtures/test'
import { SEED_PASSWORD } from './helpers'

// Creates a user, so it runs in the flows project (not the read-only observe one) and uses a unique name.
test('a new user with no permissions sees the empty state', async ({ browser, playwright, baseURL }) => {
  const username = `nobody17-${Math.random().toString(36).slice(2, 8)}`

  // Create the user through the API as sam (strict JSON body: username, admin).
  const api = await playwright.request.newContext({ baseURL: baseURL, storageState: authFile('sam') })
  const me = await (await api.get('/api/me')).json()
  const created = await api.post('/api/users', {
    headers: { 'X-CSRF-Token': me.csrf_token },
    data: { username, admin: false },
  })
  expect(created.status()).toBe(201)
  const temporaryPassword: string = (await created.json()).temporary_password
  await api.dispose()

  const { context, assertClean } = await newGuardedContext(browser, { baseURL: baseURL })
  const page = await context.newPage()
  await page.goto('/login')
  await page.getByLabel('Username').fill(username)
  await page.getByLabel('Password').fill(temporaryPassword)
  await page.getByRole('button', { name: 'Sign in' }).click()

  // The temporary password must be replaced before the app opens.
  await expect(page.getByText('Choose a new password')).toBeVisible()
  await page.getByLabel('Current password').fill(temporaryPassword)
  await page.getByLabel('New password').fill(`${SEED_PASSWORD}-new`)
  await page.getByLabel('Confirm').fill(`${SEED_PASSWORD}-new`)
  await page.getByRole('button', { name: 'Change password' }).click()

  await expect(page.getByText("You don't have access to anything yet. Ask a global admin.")).toBeVisible()
  await assertClean()
  await context.close()
})
