import type { Page } from '@playwright/test'
import { asUser, expect, test } from './fixtures/test'

// Runs in chromium-flows because it stops a task. It stops a `notifications` task, a service that only read-only specs in
// the observe project use, and the fake ECS starts a replacement, so later specs see a steady estate.
test.use(asUser('pavel'))

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
