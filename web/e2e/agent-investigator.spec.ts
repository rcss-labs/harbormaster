import type { APIRequestContext, Page } from '@playwright/test'
import { SVC, serviceUrl } from './fixtures/ids'
import { asUser, expect, test } from './fixtures/test'
import { serviceId } from './helpers'
import { createRoute, deleteE2eRoutes, openNotifications, received, startRoute, webhookBody } from './notifications.helpers'
import { saveScreenshot } from './helpers'

// Automatic investigations end to end: the dispatcher records a request when an alert opens on a switched route, the
// coordinator runs the agent service (its scripted fake model reads the service, its timeline and its logs) as the
// agent identity, and the diagnosis reaches the in-process receiver and the app. The seeded estate has image-resizer
// failing (0 of 2 tasks), so a route scoped to it is notified at once.
test.describe.configure({ mode: 'serial' })

const IMAGE_RESIZER = serviceId(...SVC.imageResizer)
const HOOK_PATH = '/fake-notify/webhook/investigated'
const SUMMARY = 'This is a canned conclusion from the demo model.'
const ALERT_TITLE = 'image-resizer: 0 of 2 tasks running'
const HIDDEN_EVIDENCE = 'Some evidence is not shown because you do not have permission to read it.'

const csrf = async (request: APIRequestContext) => ({ 'X-CSRF-Token': ((await (await request.get('/api/me')).json()) as { csrf_token: string }).csrf_token })

async function eventsAt(request: APIRequestContext, path: string): Promise<string[]> {
  return (await received(request)).filter((entry) => entry.path === path).map((entry) => entry.body.event as string)
}

async function openDiagnosis(page: Page) {
  await page.goto('/')
  const row = page.getByTestId('alert-row').filter({ hasText: ALERT_TITLE }).first()
  await row.getByRole('link', { name: 'Diagnosis ready' }).click()
  const drawer = page.getByRole('dialog', { name: 'Investigation' })
  await expect(drawer).toBeVisible()
  return drawer
}

test.describe('sam', () => {
  test.use(asUser('sam'))
  test.afterAll(async ({ request }) => deleteE2eRoutes(request))

  test('an alert on a switched route gets a diagnosis', async ({ request, baseURL }) => {
    test.setTimeout(75_000)
    await createRoute(request, webhookBody('e2e investigated', `${baseURL}${HOOK_PATH}`, { investigate: true, scope: [{ kind: 'service', service_id: IMAGE_RESIZER }] }))
    await expect.poll(async () => (await eventsAt(request, HOOK_PATH)).includes('diagnosis'), { timeout: 60_000 }).toBe(true)
    const events = await eventsAt(request, HOOK_PATH)
    expect(events.indexOf('alert.raised'), 'the alert arrives before its diagnosis').toBeGreaterThanOrEqual(0)
    expect(events.indexOf('alert.raised')).toBeLessThan(events.indexOf('diagnosis'))
    const diagnosis = (await received(request)).find((entry) => entry.path === HOOK_PATH && entry.body.event === 'diagnosis')!
    expect(diagnosis.body).toMatchObject({ ai_generated: true, service: 'image-resizer', cluster: 'background-jobs' })
    expect(diagnosis.body.investigation_id).toBeGreaterThan(0)
  })

  test('the alert on the overview opens the investigation with its summary and evidence', async ({ page }) => {
    const drawer = await openDiagnosis(page)
    await expect(drawer.getByText(ALERT_TITLE)).toBeVisible()
    await expect(drawer.getByText(SUMMARY)).toBeVisible()
    await expect(drawer.getByText('Written by AI from what its identity may read. Check it before acting.')).toBeVisible()
    const evidence = drawer.getByRole('region', { name: 'Evidence' })
    await expect(evidence.getByText('get_service', { exact: true })).toBeVisible()
    await expect(evidence.getByText('The first thing the demo model read.')).toBeVisible()
    // The cited log line is an excerpt the app stored from the agent identity's read.
    await expect(evidence.getByText('get_service_logs', { exact: true })).toBeVisible()
    await expect(evidence.getByText('The first log line the demo model read.')).toBeVisible()
    await expect(evidence.locator('pre')).toHaveCount(2)
    await expect(drawer.getByText(HIDDEN_EVIDENCE)).toHaveCount(0)
    await saveScreenshot(page, 'agent-investigation-drawer.png')
  })

  test('the investigation is on the service timeline after a reload', async ({ page }) => {
    await page.goto(serviceUrl(...SVC.imageResizer, 'timeline'))
    const list = page.getByRole('region', { name: 'Investigations' })
    await list.getByRole('button', { name: 'Diagnosis ready' }).click()
    await expect(page.getByRole('dialog', { name: 'Investigation' }).getByText(SUMMARY)).toBeVisible()
    const url = page.url()
    expect(url).toMatch(/[?&]investigation=\d+/)
    await page.reload()
    await expect(page.getByRole('dialog', { name: 'Investigation' }).getByText(SUMMARY)).toBeVisible()
    await expect(page.getByRole('region', { name: 'Investigations' }).getByRole('button', { name: 'Diagnosis ready' })).toBeVisible()
  })

  test('the users page does not list the agent identity', async ({ page }) => {
    await page.goto('/settings/users')
    await expect(page.getByRole('heading', { level: 2, name: 'Users & permissions' })).toBeVisible()
    await expect(page.getByText('dana', { exact: true }).first()).toBeVisible()
    await expect(page.getByText(/harbormaster-agent/)).toHaveCount(0)
    const users = (await (await page.request.get('/api/users')).json()) as { items?: { username: string }[]; users?: { username: string }[] }
    expect((users.items ?? users.users ?? []).map((user) => user.username).filter((name) => name.startsWith('harbormaster-agent'))).toEqual([])
  })

  test('an admin turns the switch on in the route editor', async ({ page, request, baseURL }) => {
    await openNotifications(page)
    await startRoute(page, 'e2e switch', 'Webhook')
    await page.getByLabel('Webhook URL').fill(`${baseURL}/fake-notify/webhook/switch`)
    const toggle = page.getByLabel('Investigate with AI')
    await expect(toggle).not.toBeChecked()
    await toggle.check()
    await expect(page.getByText('Your receiver will get a new event value, diagnosis.')).toBeVisible()
    await page.getByLabel('Route is on').check()
    await page.getByRole('button', { name: 'Save route' }).click()
    await expect(page.getByText('Saved "e2e switch".')).toBeVisible()
    const list = (await (await request.get('/api/notifications/routes')).json()) as { items: { name: string; investigate: boolean }[] }
    expect(list.items.find((route) => route.name === 'e2e switch')?.investigate).toBe(true)
    await page.reload()
    await page.getByRole('list', { name: 'Routes' }).getByRole('button', { name: /e2e switch/ }).click()
    await expect(page.getByLabel('Investigate with AI')).toBeChecked()
  })

  test('asks the chat about a service by name', async ({ page }) => {
    // The scripted model lists the services, takes the id of the one named, and reads it with that id: a wrong id fails the read.
    await page.goto('/clusters')
    await page.getByRole('button', { name: 'Ask AI' }).click()
    const drawer = page.getByRole('dialog', { name: 'Agent' })
    await drawer.getByRole('button', { name: 'New conversation' }).click()
    await drawer.getByRole('textbox', { name: 'Message' }).fill('What is the state of the service "image-resizer"?')
    await drawer.getByRole('button', { name: 'Send' }).click()
    const tools = drawer.getByRole('list', { name: 'Tools used' })
    await expect(tools.getByText('list_services')).toBeVisible()
    await expect(tools.getByText('get_service', { exact: true })).toBeVisible()
    await expect(drawer.getByText('image-resizer is running 0 of 2 tasks.')).toBeVisible()
  })
})

test.describe('omer', () => {
  test.use(asUser('omer'))

  // omer is an Operator on Dev; for this test he may only View, so the Logs evidence is hidden from him.
  test('a viewer without Logs sees the summary and the hidden evidence line, and no log excerpt', async ({ page, browser }) => {
    const admin = await browser.newContext({ storageState: 'e2e/.auth/sam.json' })
    try {
      const matrix = (await (await admin.request.get('/api/permissions')).json()) as { tags: { id: number; name: string }[]; users: { id: number; username: string; perms: Record<string, number> }[] }
      const dev = matrix.tags.find((tag) => tag.name === 'Dev')!
      const omer = matrix.users.find((user) => user.username === 'omer')!
      const before = omer.perms[String(dev.id)] ?? 0
      const put = async (actions: number) => {
        const response = await admin.request.put(`/api/permissions/${omer.id}/${dev.id}`, { headers: await csrf(admin.request), data: { actions } })
        expect(response.ok(), await response.text()).toBe(true)
      }
      await put(1)
      try {
        const drawer = await openDiagnosis(page)
        await expect(drawer.getByText(SUMMARY)).toBeVisible()
        await expect(drawer.getByText(HIDDEN_EVIDENCE)).toBeVisible()
        const evidence = drawer.getByRole('region', { name: 'Evidence' })
        await expect(evidence.getByText('get_service', { exact: true })).toBeVisible()
        await expect(evidence.getByText('get_service_logs')).toHaveCount(0)
        await expect(evidence.getByText('The first log line the demo model read.')).toHaveCount(0)
        await expect(evidence.locator('pre')).toHaveCount(1)
      } finally {
        await put(before)
      }
    } finally {
      await admin.close()
    }
  })
})
