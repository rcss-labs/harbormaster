import { asUser, expect, newGuardedContext, test, type Page } from './fixtures/test'
import { SEED_PASSWORD } from './helpers'

// Mutating: runs in the flows project. Everything a test creates has a unique name, and what it changes is put back.
const savedPermission = (page: Page) =>
  page.waitForResponse((response) => response.url().includes('/api/permissions/') && response.request().method() === 'PUT' && response.ok())
const unique = () => Math.random().toString(36).slice(2, 8)

test.describe('admin pages as omer', () => {
  test.use(asUser('omer'))

  test('the sidebar has no Admin section', async ({ page }) => {
    await page.goto('/')
    const nav = page.getByRole('navigation', { name: 'Main' })
    await expect(nav.getByRole('link', { name: 'My access' })).toBeVisible()
    await expect(nav.getByText('Admin', { exact: true })).toHaveCount(0)
    await expect(nav.getByRole('link', { name: 'Users and permissions' })).toHaveCount(0)
  })

  test('admin routes refuse omer', async ({ page }) => {
    await page.goto('/admin/users')
    await expect(page.getByRole('heading', { name: 'No access' })).toBeVisible()
    await expect(page.getByText('Only global admins manage users and permissions.')).toBeVisible()
    await expect(page.getByRole('table')).toHaveCount(0)

    // The API itself refuses: a 403 with the forbidden code, never data.
    const response = await page.request.get('/api/users')
    expect(response.status()).toBe(403)
    const body = await response.json()
    expect(body).toEqual({ error: { code: 'forbidden', message: expect.any(String) } })
    expect(JSON.stringify(body)).not.toMatch(/pavel|dana|oncall/)
  })
})

test.describe('admin pages as pavel', () => {
  test.use(asUser('pavel'))

  test('the permission matrix shows the seeded users and their presets', async ({ page }) => {
    await page.goto('/admin/users')
    await expect(page.getByRole('heading', { level: 1, name: 'Users and permissions' })).toBeVisible()
    for (const tag of ['Dev', 'Staging', 'Prod']) {
      await expect(page.getByRole('columnheader', { name: new RegExp(`^${tag}\\b`) })).toBeVisible()
    }
    // Users from earlier specs stay in the database, so check for these rows rather than the exact set.
    await expect(page.getByRole('row').filter({ hasText: 'Everything, on every tag' })).toContainText('pavel')
    for (const user of ['dana', 'omer', 'oncall']) {
      await expect(page.getByLabel(`Preset for ${user} on Dev`)).toBeVisible()
    }
    await expect(page.getByLabel('Preset for omer on Dev')).toHaveValue('Operator')
  })

  test('changing a preset is recorded in the audit log, then restored', async ({ page }) => {
    await page.goto('/admin/users')
    const prod = page.getByLabel('Preset for omer on Prod')
    await expect(prod).toHaveValue('None')
    try {
      // The select updates optimistically, so wait for the saved response before leaving the page.
      await Promise.all([savedPermission(page), prod.selectOption('Viewer')])
      await expect(prod).toHaveValue('Viewer')

      await page.goto('/admin/audit')
      const entry = page.getByText('Gave omer View on Prod.')
      await expect(entry).toBeVisible()
      // Newest first: the audit log has no roles, so the newest row is the first one containing the entry text, and the
      // first row mentioning both names is this one.
      await expect(page.getByText(/omer.*Prod|Prod.*omer/).first()).toHaveText('Gave omer View on Prod.')
    } finally {
      await page.goto('/admin/users')
      await Promise.all([savedPermission(page), page.getByLabel('Preset for omer on Prod').selectOption('None')])
      await expect(page.getByLabel('Preset for omer on Prod')).toHaveValue('None')
    }
  })

  test('adds a user, who must change the temporary password, then deletes them and cannot delete self', async ({ page, browser, baseURL }) => {
    const username = `nobody19-${unique()}`
    await page.goto('/admin/users')
    await page.getByRole('button', { name: 'Add user' }).click()
    const dialog = page.getByRole('dialog')
    await dialog.getByLabel('Username').fill(username)
    await expect(dialog.getByRole('checkbox')).not.toBeChecked()
    await dialog.getByRole('button', { name: 'Add user' }).click()

    await expect(dialog.getByRole('heading', { name: `Temporary password for ${username}` })).toBeVisible()
    await expect(dialog.getByText('Shown once. Copy it now.')).toBeVisible()
    const temporaryPassword = (await dialog.getByText(/^[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}$/).textContent()) ?? ''
    expect(temporaryPassword).not.toBe('')
    await dialog.getByRole('button', { name: 'Done' }).click()
    await expect(page.getByRole('row').filter({ hasText: username })).not.toHaveCount(0)

    // browser.newContext inherits the describe's storageState (pavel), so start from an empty one.
    const { context, assertClean } = await newGuardedContext(browser, { baseURL: baseURL, storageState: { cookies: [], origins: [] } })
    const other = await context.newPage()
    await other.goto('/login')
    await other.getByLabel('Username').fill(username)
    await other.getByLabel('Password').fill(temporaryPassword)
    await other.getByRole('button', { name: 'Sign in' }).click()
    await expect(other.getByText('Choose a new password')).toBeVisible()
    await other.getByLabel('Current password').fill(temporaryPassword)
    await other.getByLabel('New password').fill(`${SEED_PASSWORD}-new`)
    await other.getByLabel('Confirm').fill(`${SEED_PASSWORD}-new`)
    await other.getByRole('button', { name: 'Change password' }).click()
    await expect(other.getByText("You don't have access to anything yet. Ask a global admin.")).toBeVisible()
    await assertClean()
    await context.close()

    // The table lists the user once in the matrix and once in the users card; delete from the users card.
    const userRow = page.getByRole('row').filter({ hasText: username }).filter({ has: page.getByRole('button', { name: 'Delete' }) })
    await userRow.getByRole('button', { name: 'Delete' }).click()
    await page.getByRole('dialog').getByRole('button', { name: 'Delete user' }).click()
    await expect(page.getByRole('row').filter({ hasText: username })).toHaveCount(0)

    // Deleting yourself is refused: the button is disabled.
    const self = page.getByRole('row').filter({ hasText: '(you)' })
    await expect(self.getByRole('button', { name: 'Delete' })).toBeDisabled()
  })

  test('tags: Dev is the default, a new tag can be created, refused a duplicate name and deleted', async ({ page }) => {
    const tagName = `Canary-${unique()}`
    await page.goto('/admin/tags')
    await expect(page.getByRole('heading', { level: 1, name: 'Tags' })).toBeVisible()

    // The Dev tag is built in: neither Rename nor Delete is available.
    const dev = page.getByRole('row').filter({ has: page.getByRole('button', { name: 'Rename' }) }).filter({ hasText: /^Dev\s*default/ })
    await expect(dev.getByRole('button', { name: 'Delete' })).toBeDisabled()
    await expect(dev.getByRole('button', { name: 'Rename' })).toBeDisabled()

    await page.getByRole('button', { name: 'Create tag' }).first().click()
    let dialog = page.getByRole('dialog')
    await dialog.getByLabel('Name').fill(tagName)
    await dialog.getByLabel('green').check()
    await dialog.getByRole('button', { name: 'Create tag' }).click()
    const tagRows = page.getByRole('row').filter({ has: page.getByRole('button', { name: 'Rename' }) })
    const row = tagRows.filter({ hasText: tagName })
    await expect(row).toBeVisible()

    // A name that differs only in case from an existing tag is a duplicate.
    await row.getByRole('button', { name: 'Rename' }).click()
    dialog = page.getByRole('dialog')
    await dialog.getByLabel('Name').fill('prod')
    await dialog.getByRole('button', { name: 'Save' }).click()
    await expect(dialog.getByRole('alert')).toContainText('A tag called prod already exists.')
    await dialog.getByRole('button', { name: 'Cancel' }).click()

    await row.getByRole('button', { name: 'Delete' }).click()
    dialog = page.getByRole('dialog')
    await expect(dialog.getByRole('heading', { name: `Delete tag ${tagName}?` })).toBeVisible()
    await expect(dialog.getByText(/clusters? becomes? Dev\./)).toBeVisible()
    await dialog.getByRole('button', { name: 'Delete tag' }).click()
    await expect(tagRows.filter({ hasText: tagName })).toHaveCount(0)
  })

  test('AWS accounts: both rows with their role ARN, and a generated external ID', async ({ page }) => {
    await page.goto('/admin/accounts')
    await expect(page.getByRole('heading', { level: 1, name: 'AWS accounts' })).toBeVisible()
    const homelab = page.getByRole('row').filter({ hasText: 'homelab-admin' })
    await expect(homelab).toContainText('arn:aws:iam::482109375512:role/harbormaster-access')
    await expect(homelab).toContainText('us-east-1, eu-west-1, eu-central-1')
    const staging = page.getByRole('row').filter({ hasText: 'staging-example' })
    await expect(staging).toContainText('arn:aws:iam::731022849906:role/harbormaster-access')
    await expect(staging).toContainText('us-east-1')
    // Last check is relative time once the first poll has run.
    await expect(homelab).toContainText('ago', { timeout: 30_000 })

    await page.getByRole('button', { name: 'Connect account' }).click()
    const dialog = page.getByRole('dialog')
    const externalId = dialog.getByLabel('External ID')
    await expect(externalId).toHaveValue(/^.{24}$/)
    await dialog.getByRole('button', { name: 'Cancel' }).click()
  })
})
