import fs from 'node:fs'
import path from 'node:path'
import { authFile, expect, test as setup } from './fixtures/test'
import { USERS, type Role } from './fixtures/users'
import { login } from './helpers'

for (const role of Object.keys(USERS) as Role[]) {
  setup(`sign in as ${role}`, async ({ page }) => {
    await login(page, USERS[role].username, USERS[role].password)
    await expect(page).toHaveURL(/\/$/)
    fs.mkdirSync(path.dirname(authFile(role)), { recursive: true })
    await page.context().storageState({ path: authFile(role) })
  })
}

setup('wrong password is rejected', async ({ page }) => {
  await page.goto('/login')
  await page.getByLabel('Username').fill(USERS.sam.username)
  await page.getByLabel('Password').fill('not-the-password')
  await page.getByRole('button', { name: 'Sign in' }).click()
  await expect(page.getByText('Wrong username or password.', { exact: true })).toBeVisible()
})
