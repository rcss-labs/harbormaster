import type { Page } from '@playwright/test'
import { asUser, expect, test } from './fixtures/test'

// Runs in chromium-flows because the second test stops a task. It stops a `notifications` task, a service that only
// read-only specs in the observe project use, and the fake ECS starts a replacement, so later specs see a steady estate.
// The first test only reads (payments-api's seeded log history), so it is safe wherever the file runs.
test.describe.configure({ mode: 'serial' })
test.use(asUser('pavel'))

const RECENT_LOG_LINES = 20

/** Opens /tasks with repeats off (one row per task) and opens the drawer of the first running task of `serviceName`. */
async function openRunningTaskDrawer(page: Page, serviceName: string) {
  await page.goto('/tasks?group=0&filter=running')
  const row = page.getByRole('row').filter({ hasText: serviceName }).filter({ hasText: 'Running' }).first()
  await expect(row).toBeVisible()
  const opener = row.getByRole('button', { name: /^Open details for task / })
  const shortId = ((await opener.getAttribute('aria-label')) ?? '').replace('Open details for task ', '')
  expect(shortId).toMatch(/^[0-9a-f]{12}$/)
  await opener.click()
  const drawer = page.getByRole('dialog')
  await expect(drawer).toContainText(serviceName)
  return { drawer, shortId }
}

/** Full task ids; the UI shows only their first 12 characters. */
async function runningTaskIds(page: Page): Promise<string[]> {
  const response = await page.request.get('/api/tasks?filter=running&limit=500')
  expect(response.ok()).toBe(true)
  return ((await response.json()) as { items: { short_id: string }[] }).items.map((task) => task.short_id)
}

test('the drawer of a running payments-api task shows 20 log lines of that task', async ({ page }) => {
  const { drawer, shortId } = await openRunningTaskDrawer(page, 'payments-api')

  const logSection = drawer.locator('section').filter({ has: page.getByRole('heading', { name: 'Recent log lines' }) })
  await expect(logSection.locator('div > div')).toHaveCount(RECENT_LOG_LINES)
  await expect(drawer.getByText('No log lines.')).toHaveCount(0)

  // The same 20 lines come from the per-task logs endpoint, so the drawer is not showing a service-wide read.
  const href = await drawer.getByRole('link', { name: 'Open logs' }).getAttribute('href')
  const response = await page.request.get(`/api${href}?task=${shortId}&limit=${RECENT_LOG_LINES}`)
  expect(response.ok()).toBe(true)
  expect(((await response.json()) as { lines: unknown[] }).lines).toHaveLength(RECENT_LOG_LINES)
})

test('stopping a running task from the drawer asks for confirmation and the task leaves the running state', async ({ page }) => {
  const { drawer, shortId } = await openRunningTaskDrawer(page, 'notifications')

  await drawer.getByRole('button', { name: 'Stop task' }).click()
  const dialog = page.getByRole('dialog')
  await expect(dialog.getByRole('heading', { name: `Stop task ${shortId}?`, exact: true })).toBeVisible()
  await dialog.getByRole('button', { name: 'Stop task' }).click()
  await expect(dialog).toHaveCount(0)

  // The task leaves the running list at once or after a poll (ECS may report it as stopping first), then shows as stopped.
  await expect.poll(async () => (await runningTaskIds(page)).some((id) => id.startsWith(shortId)), { timeout: 15_000 }).toBe(false)
  await page.goto('/tasks?group=0&filter=stopped')
  await page.getByPlaceholder('Filter by task ID, service, cluster, IP').fill(shortId) // the stopped list is long and the seeded stops come first
  await expect(page.getByRole('row').filter({ hasText: shortId }).getByText(/Stopped|Stopping/).first()).toBeVisible()
})
