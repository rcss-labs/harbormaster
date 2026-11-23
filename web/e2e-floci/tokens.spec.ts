import { randomBytes } from 'node:crypto'
import type { APIRequestContext, Page, PlaywrightWorkerArgs } from '@playwright/test'
import { expectPhoneLayoutClean, watchPage } from '../e2e/fixtures/phone'
import { expect, test } from '../e2e/fixtures/test'

// Access tokens and the OAuth consent page against the floci estate, as admin (project tokens in
// playwright.floci-tokens.config.ts; its webServer runs tools/floci/serve-tokens.sh with HM_API_TOKENS and HM_OAUTH on).
// Secrets are read into variables only to be compared or used as a bearer; they are never logged or put in an assertion message.

const TOKEN_NAME = 'floci-ui-token'
const OMER_TOKEN_NAME = 'floci-omer-token'
const PHONE_TOKEN_NAME = 'floci-phone-token'
const NAME_PREFIX = 'floci-'

// The config's storageState (admin's session) also seeds request contexts, so a context meant to carry no cookie must
// say so; otherwise omer's sign-in would present admin's cookie and end admin's session.
const NO_COOKIES = { cookies: [], origins: [] }

async function csrfOf(request: APIRequestContext): Promise<string> {
  const me = await request.get('/api/me')
  expect(me.status(), 'GET /api/me').toBe(200)
  return ((await me.json()) as { csrf_token: string }).csrf_token
}

/** Deletes tokens a previous, interrupted run left behind, so the 10-token limit and name lookups stay clean. */
async function deleteStaleTokens(request: APIRequestContext) {
  const headers = { 'X-CSRF-Token': await csrfOf(request) }
  const list = await request.get('/api/tokens')
  expect(list.status(), 'GET /api/tokens (are HM_API_TOKENS on? start the server with serve-tokens.sh)').toBe(200)
  const items = ((await list.json()) as { items: { id: number; name: string }[] }).items
  for (const token of items.filter((t) => t.name.startsWith(NAME_PREFIX))) {
    await request.delete(`/api/tokens/${token.id}`, { headers })
  }
}

/** The admin resets omer's password, omer finishes the forced change and creates a one-day token. */
async function createOmerToken(admin: APIRequestContext, playwright: PlaywrightWorkerArgs['playwright'], baseURL: string, name: string) {
  const adminHeaders = { 'X-CSRF-Token': await csrfOf(admin) }
  const users = ((await (await admin.get('/api/users')).json()) as { items: { id: number; username: string }[] }).items
  const omer = users.find((u) => u.username === 'omer')
  expect(omer, 'user omer exists (connect.sh creates it)').toBeTruthy()
  const reset = await admin.post(`/api/users/${omer!.id}/reset-password`, { headers: adminHeaders })
  expect(reset.status(), 'reset omer password').toBe(200)
  const temporary = ((await reset.json()) as { temporary_password: string }).temporary_password

  const omerContext = await playwright.request.newContext({ baseURL, storageState: NO_COOKIES })
  try {
    const login = await omerContext.post('/api/auth/login', { data: { username: 'omer', password: temporary } })
    expect(login.status(), 'omer sign-in').toBe(200)
    let omerHeaders = { 'X-CSRF-Token': await csrfOf(omerContext) }
    const me = (await (await omerContext.get('/api/me')).json()) as { must_change_password: boolean }
    if (me.must_change_password) {
      const change = await omerContext.post('/api/auth/password', {
        data: { current: temporary, new: randomBytes(16).toString('hex') },
        headers: omerHeaders,
      })
      expect(change.status(), 'omer password change').toBeLessThan(300)
      omerHeaders = { 'X-CSRF-Token': await csrfOf(omerContext) }
    }
    const created = await omerContext.post('/api/tokens', { data: { name, expires_in_days: 1 }, headers: omerHeaders })
    expect(created.status(), 'create omer token').toBeLessThan(300)
  } finally {
    await omerContext.dispose()
  }
}

/** True when the page (or the given element) scrolls sideways. */
async function scrollsSideways(page: Page): Promise<boolean> {
  return page.evaluate(() => document.documentElement.scrollWidth > document.documentElement.clientWidth + 1)
}

const tokensCard = (page: Page) => page.getByRole('region', { name: 'Access tokens' })

test.describe('access tokens (desktop)', () => {
  test.describe.configure({ mode: 'serial' })
  test.use({ permissions: ['clipboard-read', 'clipboard-write'] })

  // The secret of the token the UI creates, kept to prove it works and, after revoking, that it stops.
  let secret = ''

  test.beforeAll(async ({ playwright, baseURL }) => {
    const admin = await playwright.request.newContext({ baseURL, storageState: '.auth-floci/admin.json' })
    try {
      await deleteStaleTokens(admin)
    } finally {
      await admin.dispose()
    }
  })

  test('My access shows the access tokens card', async ({ page }) => {
    await page.setViewportSize({ width: 1440, height: 900 })
    await page.goto('/me/access')
    await expect(page.getByRole('heading', { level: 1, name: 'My access' })).toBeAttached()
    const card = tokensCard(page)
    await expect(card).toBeVisible()
    await expect(card.getByRole('button', { name: 'Create token' })).toBeEnabled()
    await expect(card.getByText(/Log search by tokens today: .* of .*\./)).toBeVisible()
    // HM_OAUTH is on, so the connector hint and the Connected apps card are shown too.
    await expect(card.getByText(/In claude\.ai: Settings, Connectors, Add custom connector, paste .*\/mcp\./)).toBeVisible()
    await expect(page.getByRole('region', { name: 'Connected apps' })).toBeVisible()
    expect(await scrollsSideways(page), 'document scrolls sideways at 1440px').toBe(false)
  })

  test('creates a token: the secret is shown once with Copy and the MCP command', async ({ page, playwright, baseURL }) => {
    await page.goto('/me/access')
    await tokensCard(page).getByRole('button', { name: 'Create token' }).click()

    const form = page.getByRole('dialog', { name: 'Create access token' })
    await expect(form).toBeVisible()
    const create = form.getByRole('button', { name: 'Create token' })
    await expect(create, 'a name is required').toBeDisabled()
    await form.getByRole('textbox', { name: 'Name' }).fill(TOKEN_NAME)
    await form.getByLabel('Lifetime').selectOption({ label: '1 day' })
    await create.click()

    const created = page.getByRole('dialog', { name: 'Token created' })
    await expect(created).toBeVisible()
    await expect(created.getByText('This is the only time the token is shown.')).toBeVisible()
    const secretBox = created.getByTestId('token-secret')
    await expect(secretBox).toHaveText(/^hmp_\S+$/)
    secret = ((await secretBox.textContent()) ?? '').trim()

    const command = created.getByText(/^claude mcp add --transport http harbormaster \S+\/mcp --header "Authorization: Bearer hmp_\S+"$/)
    await expect(command).toBeVisible()
    await expect(created.getByText('Run this to connect Claude Code.')).toBeVisible()
    const copyButtons = created.getByRole('button', { name: 'Copy' })
    await expect(copyButtons).toHaveCount(2)
    await copyButtons.first().click()
    await expect(page.getByText('Token copied')).toBeVisible()
    await copyButtons.last().click()
    await expect(page.getByText('Command copied')).toBeVisible()

    // The token really works as a bearer on a read route (a fresh context: no cookie to fall back on).
    const bare = await playwright.request.newContext({ baseURL, storageState: NO_COOKIES })
    try {
      const current = await bare.get('/api/tokens/current', { headers: { Authorization: `Bearer ${secret}` } })
      expect(current.status(), 'the new token reads /api/tokens/current').toBe(200)
      const restart = await bare.post('/api/services/any/restart', { headers: { Authorization: `Bearer ${secret}` } })
      expect(restart.status(), 'the new token cannot change anything').toBe(403)
    } finally {
      await bare.dispose()
    }

    await created.getByRole('button', { name: 'Done' }).click()
    await expect(created).toBeHidden()

    // The list row appears; the secret is gone from the page, also after a reload.
    const card = tokensCard(page)
    await expect(card.getByText(TOKEN_NAME, { exact: true })).toBeVisible()
    await expect(card.getByText(/^hmp_\S+$/).first(), 'row shows the public id').toBeVisible()
    await expect(card.getByText('Never used')).toBeVisible()
    expect((await page.content()).includes(secret), 'secret left in the DOM after Done').toBe(false)
    await page.reload()
    await expect(tokensCard(page).getByText(TOKEN_NAME, { exact: true })).toBeVisible()
    expect((await page.content()).includes(secret), 'secret in the DOM after reload').toBe(false)
  })

  test('an admin may choose at most 7 days, in the UI and in the API', async ({ page }) => {
    await page.goto('/me/access')
    await tokensCard(page).getByRole('button', { name: 'Create token' }).click()
    const form = page.getByRole('dialog', { name: 'Create access token' })
    const lifetime = form.getByLabel('Lifetime')
    await expect(lifetime.locator('option')).toHaveText(['1 day', '7 days'])
    await expect(lifetime).toHaveValue('7')
    await expect(form.getByText('Tokens of global admins last at most 7 days.')).toBeVisible()
    await form.getByRole('button', { name: 'Cancel' }).click()
    await expect(form).toBeHidden()

    const headers = { 'X-CSRF-Token': await csrfOf(page.request) }
    const tooLong = await page.request.post('/api/tokens', { data: { name: 'floci-too-long', expires_in_days: 30 }, headers })
    expect(tooLong.status(), 'a 30 day token is refused').toBeGreaterThanOrEqual(400)
    expect(tooLong.status()).toBeLessThan(500)
    expect(JSON.stringify(await tooLong.json())).toContain('1 to 7 days')
  })

  test('revokes the token and it stops working', async ({ page, playwright, baseURL }) => {
    await page.goto('/me/access')
    const card = tokensCard(page)
    await card.getByRole('button', { name: `Revoke ${TOKEN_NAME}` }).click()
    const confirm = page.getByRole('dialog', { name: `Revoke ${TOKEN_NAME}?` })
    await expect(confirm).toBeVisible()
    await confirm.getByRole('button', { name: 'Revoke token' }).click()
    await expect(page.getByText(`Revoked ${TOKEN_NAME}`)).toBeVisible()
    await expect(card.getByText(TOKEN_NAME, { exact: true })).toHaveCount(0)

    expect(secret, 'the create test ran first').not.toBe('')
    const bare = await playwright.request.newContext({ baseURL, storageState: NO_COOKIES })
    try {
      const current = await bare.get('/api/tokens/current', { headers: { Authorization: `Bearer ${secret}` } })
      expect(current.status(), 'the revoked token is refused').toBe(401)
    } finally {
      await bare.dispose()
    }
  })

  test('admin opens Users, then omer, then the tokens dialog and revokes from there', async ({ page, playwright, baseURL }) => {
    await createOmerToken(page.request, playwright, baseURL!, OMER_TOKEN_NAME)

    await page.setViewportSize({ width: 1440, height: 900 })
    await page.goto('/settings/users')
    // The permissions matrix has an omer row too; the users table's row is the one with his Actions menu.
    const row = page.getByRole('row').filter({ has: page.getByRole('button', { name: 'Actions for omer' }) })
    await expect(row).toBeVisible()
    await row.getByRole('button', { name: 'Actions for omer' }).click()
    await page.getByRole('menuitem', { name: /^Access tokens \(\d+\)…$/ }).click()

    const dialog = page.getByRole('dialog', { name: 'Access tokens of omer' })
    await expect(dialog).toBeVisible()
    await expect(dialog.getByText(OMER_TOKEN_NAME, { exact: true })).toBeVisible()
    await expect(dialog.getByText(/^hmp_\S+$/).first()).toBeVisible()

    await dialog.getByRole('button', { name: `Revoke ${OMER_TOKEN_NAME}` }).click()
    await page.getByRole('dialog', { name: `Revoke ${OMER_TOKEN_NAME}?` }).getByRole('button', { name: 'Revoke token' }).click()
    await expect(page.getByText(`Revoked ${OMER_TOKEN_NAME}`)).toBeVisible()
    await expect(page.getByRole('dialog', { name: 'Access tokens of omer' }).getByText('omer has no tokens.')).toBeVisible()
    await page.getByRole('dialog', { name: 'Access tokens of omer' }).getByRole('button', { name: 'Close' }).click()
  })

  test('the OAuth consent route shows an error state for an invalid request, without crashing', async ({ page }) => {
    const errors: string[] = []
    page.on('pageerror', (error) => errors.push(error.message))

    const invalid = new URLSearchParams({
      response_type: 'code',
      client_id: 'http://localhost/client.json',
      redirect_uri: 'http://127.0.0.1:9/callback',
      code_challenge: 'x'.repeat(43),
      code_challenge_method: 'S256',
      state: 'floci',
    })
    await page.goto(`/oauth/authorize?${invalid.toString()}`)
    await expect(page.getByRole('heading', { name: 'This request cannot be approved' })).toBeVisible()
    await expect(page.getByRole('alert')).toContainText(/application/i)
    await expect(page.getByRole('button', { name: 'Approve' })).toHaveCount(0)
    await expect(page.getByRole('button', { name: 'Deny' })).toHaveCount(0)
    // The browser stays on Harbormaster: an unvalidated client is never sent anything.
    expect(new URL(page.url()).pathname).toBe('/oauth/authorize')

    // No parameters at all is also just an error page.
    await page.goto('/oauth/authorize')
    await expect(page.getByRole('heading', { name: 'This request cannot be approved' })).toBeVisible()
    await expect(page.getByRole('button', { name: 'Approve' })).toHaveCount(0)
    expect(errors, 'uncaught page errors').toEqual([])
  })
})

test.describe('access tokens (phone, 390px)', () => {
  test.use({ viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true, deviceScaleFactor: 3, permissions: ['clipboard-read', 'clipboard-write'] })

  test('the tokens card, the create dialog and the secret fit the screen', async ({ page }) => {
    watchPage(page)
    await page.goto('/me/access')
    const card = tokensCard(page)
    await expect(card).toBeVisible()
    await expect(card.getByRole('button', { name: 'Create token' })).toBeVisible()
    expect(await scrollsSideways(page), 'document scrolls sideways at 390px (empty list)').toBe(false)
    await expectPhoneLayoutClean(page)

    await card.getByRole('button', { name: 'Create token' }).click()
    const form = page.getByRole('dialog', { name: 'Create access token' })
    await expect(form).toBeVisible()
    await form.getByRole('textbox', { name: 'Name' }).fill(PHONE_TOKEN_NAME)
    await form.getByLabel('Lifetime').selectOption({ label: '1 day' })
    await form.getByRole('button', { name: 'Create token' }).click()

    // The long secret and the claude mcp add command must wrap or scroll inside the dialog, not widen the page.
    const created = page.getByRole('dialog', { name: 'Token created' })
    await expect(created).toBeVisible()
    await expect(created.getByTestId('token-secret')).toHaveText(/^hmp_/)
    expect(await scrollsSideways(page), 'document scrolls sideways with the secret shown').toBe(false)
    const box = await created.boundingBox()
    expect(box, 'dialog has a box').not.toBeNull()
    expect(box!.x).toBeGreaterThanOrEqual(0)
    expect(box!.x + box!.width).toBeLessThanOrEqual(390 + 1)
    await created.getByRole('button', { name: 'Done' }).click()

    // The list with a row on the phone.
    await expect(card.getByText(PHONE_TOKEN_NAME, { exact: true })).toBeVisible()
    expect(await scrollsSideways(page), 'document scrolls sideways at 390px (with a token)').toBe(false)
    await expectPhoneLayoutClean(page)

    // Clean up.
    await card.getByRole('button', { name: `Revoke ${PHONE_TOKEN_NAME}` }).click()
    await page.getByRole('dialog', { name: `Revoke ${PHONE_TOKEN_NAME}?` }).getByRole('button', { name: 'Revoke token' }).click()
    await expect(card.getByText(PHONE_TOKEN_NAME, { exact: true })).toHaveCount(0)
  })
})
