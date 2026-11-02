import type { APIResponse } from '@playwright/test'
import { asUser, expect, newGuardedContext, test, type Page } from './fixtures/test'
import { SEED_PASSWORD } from './helpers'

// Mutating: runs in the flows project. The steps share state (SSO configured, then enabled), so they run in order and
// stop at the first failure; `finally` puts the database back so later specs and later runs start clean.

const NO_ACCESS = "You don't have access to anything yet. Ask a global admin."

/** The Users card row (the permission matrix above it also has one per user, without buttons). The first cell reads
 * as the user name, then " (you)" or the e-mail address (the e-mail is a separate element, so no separator). */
const userRow = (page: Page, username: string) =>
  page
    .getByRole('row')
    .filter({ hasText: new RegExp(`^${username}(?:\\s|${username}@|(?![a-z0-9.-]))`) })
    .filter({ has: page.getByRole('button', { name: 'Delete' }) })

/** Completes the fake IdP form: it auto-posts the signed response back to Harbormaster. */
async function signInAtFakeIdP(page: Page, user: string) {
  await page.getByLabel('Sign in as').fill(user)
  await page.getByRole('button', { name: 'Continue' }).click()
}

async function startContext(browser: Parameters<typeof newGuardedContext>[0], baseURL: string | undefined) {
  const guarded = await newGuardedContext(browser, { baseURL, storageState: { cookies: [], origins: [] } })
  return { ...guarded, page: await guarded.context.newPage() }
}

async function ssoSignIn(page: Page, user: string) {
  await page.goto('/login')
  await page.getByRole('link', { name: 'Sign in with SSO' }).click()
  await signInAtFakeIdP(page, user)
}

const savedPermission = (page: Page) =>
  page.waitForResponse((response) => response.url().includes('/api/permissions/') && response.request().method() === 'PUT' && response.ok())

test.describe.serial('single sign-on against the fake identity provider', () => {
  test.use(asUser('pavel'))

  test('configure, test, link, enable, sign in, replay, unlink', async ({ page, browser, baseURL, playwright }) => {
    test.setTimeout(180_000)
    const audience = baseURL ?? ''
    try {
      // (1) Configure from the fake IdP metadata.
      await page.goto('/settings/sso')
      await page.getByLabel('Metadata URL').fill(`${audience}/fake-idp/metadata`)
      await page.getByRole('button', { name: 'Fetch' }).click()
      await expect(page.getByText(`${audience}/fake-idp`, { exact: true })).toBeVisible()
      await expect(page.getByText(`${audience}/fake-idp/sso`, { exact: true })).toBeVisible()
      await expect(page.getByText('SSO off', { exact: true })).toBeVisible()

      // (2) Test sign-in opens a popup that ends on the settings page with the result.
      const popupPromise = page.waitForEvent('popup')
      await page.getByRole('link', { name: 'Test sign-in' }).click()
      const popup = await popupPromise
      await signInAtFakeIdP(popup, 'alex')
      await popup.waitForURL(/\/settings\/sso\?test=/)
      await expect(popup.getByText('Sign-in test passed.')).toBeVisible()
      await expect(popup.getByText('Would create user alex')).toBeVisible()
      await popup.close()

      // (3) Self-link while SSO is still off.
      await page.goto('/')
      await page.getByRole('button', { name: 'Link SSO account' }).click()
      const linkDialog = page.getByRole('dialog', { name: 'Link your SSO account' })
      await linkDialog.getByLabel('Password').fill(SEED_PASSWORD)
      await linkDialog.getByRole('button', { name: 'Continue to identity provider' }).click()
      await signInAtFakeIdP(page, 'pavel')
      await expect(page).toHaveURL(/\/(\?.*)?$/)
      await expect(page.getByText('Your SSO account is linked.')).toBeVisible()
      await page.goto('/settings/users')
      await expect(userRow(page, 'pavel')).toContainText('Password')
      await expect(userRow(page, 'pavel')).toContainText('SSO linked')

      // (4) Pending link for dana.
      await userRow(page, 'dana').getByRole('button', { name: 'Link SSO…' }).click()
      const pendingDialog = page.getByRole('dialog')
      await pendingDialog.getByLabel('NameID').fill('dana@example.test')
      await pendingDialog.getByRole('button', { name: 'Set pending link' }).click()
      await expect(userRow(page, 'dana')).toContainText('Link pending')

      // (5) Break-glass for pavel, so turning SSO on cannot lock the admins out.
      await userRow(page, 'pavel').getByRole('button', { name: 'Make break-glass' }).click()
      await page.getByRole('dialog').getByRole('button', { name: 'Make break-glass' }).click()
      await expect(userRow(page, 'pavel')).toContainText('Break-glass')

      // (6) Turn SSO on.
      await page.goto('/settings/sso')
      await page.getByRole('button', { name: 'Turn on single sign-on' }).click()
      await page.getByRole('dialog').getByRole('button', { name: 'Turn on' }).click()
      await expect(page.getByText('SSO on', { exact: true })).toBeVisible()

      // (7) dana signs in with SSO and keeps her account and permissions.
      {
        const dana = await startContext(browser, baseURL)
        await ssoSignIn(dana.page, 'dana')
        await expect(dana.page).toHaveURL(/\/(\?.*)?$/)
        await expect(dana.page.getByText('Signed in as')).toContainText('dana')
        await dana.page.goto('/clusters')
        await expect(dana.page.getByRole('article')).toHaveCount(6)
        await dana.assertClean()
        await dana.context.close()
      }
      await page.goto('/settings/users')
      await expect(userRow(page, 'dana')).toContainText('SSO linked')
      await expect(userRow(page, 'dana')).toContainText('Password')

      // (8) alex is created on first sign-in, sees nothing until granted access. Capture the ACS POST for the replay.
      let acsBody = ''
      {
        const alex = await startContext(browser, baseURL)
        alex.page.on('request', (request) => {
          if (request.method() === 'POST' && new URL(request.url()).pathname === '/api/auth/saml/acs') acsBody = request.postData() ?? ''
        })
        await ssoSignIn(alex.page, 'alex')
        await expect(alex.page.getByText(NO_ACCESS)).toBeVisible()

        await page.goto('/settings/users')
        const preset = page.getByLabel('Preset for alex on Dev')
        await Promise.all([savedPermission(page), preset.selectOption('Viewer')])
        await expect(preset).toHaveValue('Viewer')

        await alex.page.reload()
        await alex.page.goto('/clusters')
        await expect(alex.page.getByRole('article').first()).toBeVisible()
        await alex.assertClean()
        await alex.context.close()
      }

      // (9) Password sign-in is for break-glass accounts only.
      {
        const omer = await startContext(browser, baseURL)
        await omer.page.goto('/login')
        await omer.page.getByRole('button', { name: 'Sign in with a password instead' }).click()
        await expect(omer.page.getByText('Only break-glass accounts can sign in with a password.')).toBeVisible()
        await omer.page.getByLabel('Username').fill('omer')
        await omer.page.getByLabel('Password').fill(SEED_PASSWORD)
        await omer.page.getByRole('button', { name: 'Sign in' }).click()
        await expect(omer.page.getByText('Use single sign-on to sign in to this account.')).toBeVisible()
        await omer.assertClean()
        await omer.context.close()

        const pavelPassword = await startContext(browser, baseURL)
        await pavelPassword.page.goto('/login')
        await pavelPassword.page.getByRole('button', { name: 'Sign in with a password instead' }).click()
        await pavelPassword.page.getByLabel('Username').fill('pavel')
        await pavelPassword.page.getByLabel('Password').fill(SEED_PASSWORD)
        await pavelPassword.page.getByRole('button', { name: 'Sign in' }).click()
        await expect(pavelPassword.page.getByRole('navigation', { name: 'Main' })).toBeVisible()
        await pavelPassword.assertClean()
        await pavelPassword.context.close()

        const pavelSso = await startContext(browser, baseURL)
        await ssoSignIn(pavelSso.page, 'pavel')
        await expect(pavelSso.page.getByRole('navigation', { name: 'Main' })).toBeVisible()
        await expect(pavelSso.page.getByText('Signed in as')).toContainText('pavel')
        await expect(pavelSso.page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'Settings' })).toBeVisible()
        await pavelSso.assertClean()
        await pavelSso.context.close()
      }

      // (10) A captured ACS POST cannot be replayed.
      expect(acsBody, 'captured ACS body').toContain('SAMLResponse=')
      {
        const anonymous = await playwright.request.newContext({ baseURL })
        const replay = await anonymous.post('/api/auth/saml/acs', {
          headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
          data: acsBody,
          maxRedirects: 0,
        })
        expect(replay.status()).toBe(303)
        expect(replay.headers()['location']).toContain('/login?error=sso_failed')
        await anonymous.dispose()
      }

      // (11) The audit log tells the story.
      await page.goto('/settings/audit')
      for (const entry of [
        'Created user alex from SSO.',
        'Signed in with SSO.',
        'Linked pavel to SSO identity pavel@example.test.',
        'Linked dana to SSO identity dana@example.test.',
      ]) {
        await expect(page.getByText(entry).first()).toBeVisible()
      }

      // (12) Unlink: alex has no password, dana keeps hers.
      await page.goto('/settings/users')
      await userRow(page, 'alex').getByRole('button', { name: 'Unlink SSO…' }).click()
      let confirm = page.getByRole('dialog')
      await expect(confirm.getByText('They get a temporary password and must choose their own at next sign-in.')).toBeVisible()
      await confirm.getByRole('button', { name: 'Unlink' }).click()
      await expect(page.getByText(/New temporary password for alex: /)).toBeVisible()

      await userRow(page, 'dana').getByRole('button', { name: 'Unlink SSO…' }).click()
      confirm = page.getByRole('dialog')
      await expect(confirm.getByText('They keep their password.')).toBeVisible()
      await confirm.getByRole('button', { name: 'Unlink' }).click()
      await expect(userRow(page, 'dana')).not.toContainText('SSO linked')
      await expect(page.getByText(/New temporary password for dana/)).toHaveCount(0)

      // Turn SSO off; dana's seed password works again, proving nothing was cleared.
      await page.goto('/settings/sso')
      await page.getByRole('button', { name: 'Turn off single sign-on' }).click()
      await page.getByRole('dialog').getByRole('button', { name: 'Turn off' }).click()
      await expect(page.getByText('SSO off', { exact: true })).toBeVisible()
      {
        const dana = await startContext(browser, baseURL)
        await dana.page.goto('/login')
        await dana.page.getByLabel('Username').fill('dana')
        await dana.page.getByLabel('Password').fill(SEED_PASSWORD)
        await dana.page.getByRole('button', { name: 'Sign in' }).click()
        await expect(dana.page.getByRole('navigation', { name: 'Main' })).toBeVisible()
        await dana.assertClean()
        await dana.context.close()
      }

      // Leave pavel as the seed left him: unlinked, no break-glass flag. (alex is deleted below.)
      await page.goto('/settings/users')
      await userRow(page, 'pavel').getByRole('button', { name: 'Unlink SSO…' }).click()
      await expect(page.getByRole('dialog').getByText('They keep their password.')).toBeVisible()
      await page.getByRole('dialog').getByRole('button', { name: 'Unlink' }).click()
      await expect(userRow(page, 'pavel')).not.toContainText('SSO linked')
      await userRow(page, 'pavel').getByRole('button', { name: 'Remove break-glass' }).click()
      await page.getByRole('dialog').getByRole('button', { name: 'Remove break-glass' }).click()
      await expect(userRow(page, 'pavel')).not.toContainText('Break-glass')
      await userRow(page, 'alex').getByRole('button', { name: 'Delete' }).click()
      await page.getByRole('dialog').getByRole('button', { name: 'Delete user' }).click()
      await expect(userRow(page, 'alex')).toHaveCount(0)
    } finally {
      // Idempotent cleanup through the API, so a failure halfway still leaves later specs and runs a clean database.
      const api = page.request
      // Every cleanup call must succeed (404 where the thing is already gone), so a failed cleanup fails loudly.
      const expectCleaned = (response: APIResponse, what: string) => {
        expect(response.ok() || response.status() === 404, `cleanup: ${what} returned ${response.status()}`).toBe(true)
      }
      expectCleaned(await api.put('/api/sso/enabled', { data: { enabled: false } }), 'disable SSO')
      const users = await api.get('/api/users')
      expect(users.ok(), `cleanup: list users returned ${users.status()}`).toBe(true)
      const body = (await users.json()) as { items?: Array<{ id: number; username: string; sso: string; break_glass: boolean }> } | Array<{ id: number; username: string; sso: string; break_glass: boolean }>
      const list = Array.isArray(body) ? body : (body.items ?? [])
      for (const user of list) {
        if (user.username === 'alex') {
          expectCleaned(await api.delete(`/api/users/${user.id}`), 'delete alex')
          continue
        }
        if (user.sso === 'linked') expectCleaned(await api.post(`/api/users/${user.id}/sso-unlink`), `unlink ${user.username}`)
        else if (user.sso === 'pending' || user.sso === 'pending_expired') expectCleaned(await api.delete(`/api/users/${user.id}/sso-link`), `clear link of ${user.username}`)
        if (user.break_glass) expectCleaned(await api.post(`/api/users/${user.id}/break-glass`, { data: { break_glass: false } }), `remove break-glass of ${user.username}`)
      }
    }
  })
})
