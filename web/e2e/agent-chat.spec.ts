import type { APIRequestContext, Page } from '@playwright/test'
import { asUser, expect, test } from './fixtures/test'
import { saveScreenshot } from './helpers'

// The agent runs on its scripted fake model (HM_AGENT_FAKE_PROVIDER): it calls get_overview, then gives a canned answer.
const CANNED_ANSWER = 'This is a canned answer from the demo model.'
const USED_UP = "This month's AI allowance is used up. Ask an admin to raise it."

const askAI = (page: Page) => page.getByRole('button', { name: 'Ask AI' })
const drawer = (page: Page) => page.getByRole('dialog', { name: 'Agent' })

async function ask(page: Page, question: string) {
  await askAI(page).click()
  // The drawer opens on the latest conversation once the list loads; a new one keeps every run's text apart.
  await drawer(page).getByRole('button', { name: 'New conversation' }).click()
  await drawer(page).getByRole('textbox', { name: 'Message' }).fill(question)
  await drawer(page).getByRole('button', { name: 'Send' }).click()
  await expect(drawer(page).getByText(question, { exact: true })).toBeVisible()
}

interface SettingsBody {
  version: number
  monthly_budget_usd: string
  unredacted_providers: string[]
  role_models: Record<string, { provider: string; model: string; available?: boolean }>
}

/** Sets the monthly allowance through the admin API and returns the value it replaced. */
async function setAllowance(request: APIRequestContext, usd: string): Promise<string> {
  const me = (await (await request.get('/api/me')).json()) as { csrf_token: string }
  const current = (await (await request.get('/api/agent/settings')).json()) as SettingsBody
  const res = await request.put('/api/agent/settings', {
    headers: { 'X-CSRF-Token': me.csrf_token },
    data: {
      version: current.version,
      monthly_budget_usd: usd,
      unredacted_providers: current.unredacted_providers,
      // The read form carries `available`, which the strict write form refuses.
      role_models: Object.fromEntries(Object.entries(current.role_models).map(([role, { provider, model }]) => [role, { provider, model }])),
    },
  })
  expect(res.ok(), `PUT /api/agent/settings: ${res.status()}`).toBe(true)
  return current.monthly_budget_usd
}

test.describe('agent chat as sam', () => {
  test.use(asUser('sam'))

  test('asks a question and sees a streamed answer with a tool name', async ({ page }) => {
    await page.goto('/clusters')
    await ask(page, 'How is the estate doing?')
    await expect(drawer(page).getByRole('list', { name: 'Tools used' }).getByText('get_overview')).toBeVisible()
    await expect(drawer(page).getByText(CANNED_ANSWER)).toBeVisible()
    await saveScreenshot(page, 'agent-drawer.png')
  })

  test('the conversation is still there after a reload', async ({ page }) => {
    await page.goto('/clusters')
    await ask(page, 'Remember this question')
    await expect(drawer(page).getByText(CANNED_ANSWER)).toBeVisible()
    await page.reload()
    await askAI(page).click()
    await expect(drawer(page).getByText('Remember this question')).toBeVisible()
    await expect(drawer(page).getByText(CANNED_ANSWER)).toBeVisible()
  })

  test('with the allowance at 0 the drawer shows the used-up message', async ({ page }) => {
    const before = await setAllowance(page.request, '0')
    try {
      await page.goto('/clusters')
      await ask(page, 'Is anyone there?')
      await expect(drawer(page).getByRole('alert')).toHaveText(USED_UP)
    } finally {
      await setAllowance(page.request, before)
    }
  })

  test('an admin sees the fake model in Settings and runs Test model', async ({ page }) => {
    await page.goto('/settings/agent')
    await expect(page.getByRole('combobox', { name: 'Model' })).toHaveValue('fake-1')
    await page.getByRole('button', { name: 'Test model' }).click()
    await expect(page.getByRole('status').filter({ hasText: /\S/ })).not.toContainText(/fail|could not|error/i)
    await expect(page.getByRole('status').filter({ hasText: /\S/ })).toBeVisible()
    await saveScreenshot(page, 'agent-settings.png')
  })
})

test.describe('agent chat as a user without AI chat', () => {
  test.use(asUser('dana'))

  test('a user without AI chat sees no button', async ({ page }) => {
    await page.goto('/clusters')
    await expect(page.getByRole('navigation', { name: 'Main' })).toBeVisible()
    await expect(askAI(page)).toHaveCount(0)
  })
})

test.describe('pages with the agent on', () => {
  test.use(asUser('dana'))

  test('other pages log no console errors', async ({ page }) => {
    const errors: string[] = []
    page.on('console', (message) => {
      if (message.type() === 'error') errors.push(message.text())
    })
    page.on('pageerror', (error) => errors.push(error.message))
    for (const path of ['/clusters', '/services', '/tasks', '/settings/general']) {
      await page.goto(path)
      await expect(page.getByRole('navigation', { name: 'Main' })).toBeVisible()
      await page.waitForLoadState('networkidle')
    }
    expect(errors).toEqual([])
  })
})
