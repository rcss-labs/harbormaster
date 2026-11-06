import { expectSheetWithinViewport } from './fixtures/phone'
import { asUser, expect, test } from './fixtures/test'

// Stops a task, so it runs after the flow specs (chromium-mobile-flows). It stops a `notifications` task; the fake ECS starts a
// replacement, so later specs see a steady estate.
test.use({ ...asUser('sam'), viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true, deviceScaleFactor: 3 })

test('stopping a task from its sheet asks for confirmation and the task leaves the running list', async ({ page }) => {
  await page.goto('/tasks?filter=running&group=0')
  const row = page.getByRole('row').filter({ hasText: 'notifications' }).first()
  await expect(row).toBeVisible()
  const trigger = row.getByRole('button', { name: /^Actions for task / })
  const shortId = ((await trigger.getAttribute('aria-label')) ?? '').replace('Actions for task ', '')
  expect(shortId).toMatch(/^[0-9a-f]{12}$/)

  await trigger.click()
  await page.getByRole('menuitem', { name: 'Stop task' }).click()
  const confirm = page.getByRole('dialog')
  await expect(confirm.getByRole('heading', { name: `Stop task ${shortId}?`, exact: true })).toBeVisible()
  await expectSheetWithinViewport(page)
  await confirm.getByRole('button', { name: 'Stop task' }).click()
  await expect(confirm).toHaveCount(0)

  await expect
    .poll(
      async () => {
        const response = await page.request.get('/api/tasks?filter=running&limit=500')
        return ((await response.json()) as { items: { short_id: string }[] }).items.some((task) => task.short_id.startsWith(shortId))
      },
      { timeout: 15_000 },
    )
    .toBe(false)
})
