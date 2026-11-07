import { asUser, expect, test } from './fixtures/test'
import { measureBox } from './helpers'

// Read-only: runs in the observe project, before change.spec touches image-resizer.

test.describe('tasks as sam', () => {
  test.use(asUser('sam'))

  test('Problems first lists the image-resizer task stopped 4 min ago and hides the older ones', async ({ page }) => {
    await page.goto('/tasks')
    const stoppedResizer = page.getByRole('row').filter({ hasText: 'image-resizer' }).filter({ hasText: 'Stopped (exit 137)' })
    await expect(stoppedResizer.first()).toContainText('OutOfMemoryError')
    await expect.poll(() => stoppedResizer.count()).toBeGreaterThanOrEqual(1)
    // The 16 to 64 minute seeded stops are outside the default 10-minute window. Check the API's stopped_at instead of
    // the rendered "N min ago" text: that text rounds, so a stop just inside the window can already read "10 min ago".
    const tenMinutesAndSlack = 10 * 60_000 + 30_000
    await expect
      .poll(async () => {
        const response = await page.request.get('/api/tasks?filter=stopped&limit=500')
        const { items } = (await response.json()) as { items: { stopped_at: string | null }[] }
        return items.length > 0 && items.every((task) => task.stopped_at !== null && Date.now() - Date.parse(task.stopped_at) <= tenMinutesAndSlack)
      })
      .toBe(true)
    await expect(page.getByText('Stopped tasks older than 10 min are hidden.')).toBeVisible()
    await expect(page.getByRole('link', { name: 'Change', exact: true })).toBeVisible()

    await page.getByRole('radiogroup', { name: 'Task filter' }).getByRole('radio', { name: /^Running/ }).click()
    await expect(page).toHaveURL(/filter=running/)
    await expect(page.getByRole('row').filter({ hasText: 'Running' }).first()).toBeVisible()
    await expect(page.getByRole('row').filter({ hasText: 'Stopped (exit 137)' })).toHaveCount(0)
  })
})

test.describe('task drawer and layout as sam', () => {
  test.use(asUser('sam'))

  test('a task id opens the drawer with the stop reason; Esc closes it', async ({ page }) => {
    // Repeats off, so the row is a task and not a "×N tasks" group.
    await page.goto('/tasks?group=0')
    const stoppedResizer = page.getByRole('row').filter({ hasText: 'image-resizer' }).filter({ hasText: 'Stopped (exit 137)' }).first()
    await expect(stoppedResizer).toBeVisible()
    await stoppedResizer.getByRole('button', { name: /^Open details for task / }).click()
    const drawer = page.getByRole('dialog')
    await expect(drawer.getByText('Reported by ECS')).toBeVisible()
    await expect(drawer.getByText('Identity')).toBeVisible()
    await expect(drawer.getByRole('link', { name: 'Open logs' })).toBeVisible()
    await page.keyboard.press('Escape')
    await expect(drawer).toHaveCount(0)
  })

  // Needs the seeded tasks: the fake writes one line per task every 9 s, so a replacement task started by a flow spec has too few.
  test('the drawer of a running payments-api task shows 20 log lines of that task', async ({ page }) => {
    const recentLogLines = 20
    await page.goto('/tasks?group=0&filter=running')
    const row = page.getByRole('row').filter({ hasText: 'payments-api' }).filter({ hasText: 'Running' }).first()
    const opener = row.getByRole('button', { name: /^Open details for task / })
    const shortId = ((await opener.getAttribute('aria-label')) ?? '').replace('Open details for task ', '')
    await opener.click()
    const drawer = page.getByRole('dialog')
    await expect(drawer).toContainText('payments-api')

    const logSection = drawer.locator('section').filter({ has: page.getByRole('heading', { name: 'Recent log lines' }) })
    await expect(logSection.locator('div > div')).toHaveCount(recentLogLines)
    await expect(drawer.getByText('No log lines.')).toHaveCount(0)

    // The same lines come from the per-task logs endpoint, so the drawer is not showing a service-wide read.
    const href = await drawer.getByRole('link', { name: 'Open logs' }).getAttribute('href')
    const response = await page.request.get(`/api${href}?task=${shortId}&limit=${recentLogLines}`)
    expect(response.ok()).toBe(true)
    expect(((await response.json()) as { lines: unknown[] }).lines).toHaveLength(recentLogLines)
  })

  test('at 1440 the KPI strip and the pinned row actions stay visible, with no clipped KPI text', async ({ page }) => {
    await page.setViewportSize({ width: 1440, height: 900 })
    await page.goto('/tasks?filter=running&group=0')
    const kpis = page.getByRole('list', { name: 'Task summary' })
    await expect(kpis).toBeVisible()
    const clipped = await kpis.locator('li').evaluateAll((cells) => cells.filter((cell) => cell.scrollWidth > cell.clientWidth).length)
    expect(clipped).toBe(0)

    const stop = page.getByRole('button', { name: 'Stop', exact: true }).first()
    await expect(stop).toBeVisible()
    const box = await measureBox(stop)
    expect(box.x + box.width).toBeLessThanOrEqual(1440)
    // The page itself never scrolls sideways; only the table does.
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)
    expect(overflow).toBeLessThanOrEqual(0)
  })
})

test.describe('expected stops as sam', () => {
  test.use(asUser('sam'))

  test('a Spot-interrupted task that exited 1 shows the "spot" chip, a teal stripe and is not an error', async ({ page }) => {
    await page.goto('/tasks?filter=stopped&group=0')
    const spotRow = page.getByRole('row').filter({ hasText: 'web-frontend' }).filter({ hasText: 'Your Spot Task was interrupted.' })
    await expect(spotRow.first()).toBeVisible()
    await expect(spotRow.first()).toContainText('Stopped (exit 1)')
    await expect(spotRow.first().getByText('spot', { exact: true })).toBeVisible()
    await expect(spotRow.first()).toHaveAttribute('data-status', 'expected')
    const deployRow = page.getByRole('row').filter({ hasText: 'auth-service' }).filter({ hasText: 'Scaling activity initiated by (deployment' })
    await expect(deployRow.first().getByText('scaling', { exact: true })).toBeVisible()

    const response = await page.request.get('/api/tasks?filter=problems&limit=500')
    const { items } = (await response.json()) as { items: { status: string; stop_kind: string; stop_cause: string }[] }
    expect(items.some((task) => task.stop_kind === 'error')).toBe(true)
    expect(items.some((task) => task.stop_kind === 'expected' || task.stop_cause === 'spot')).toBe(false)
  })
})

test.describe('tasks as omer', () => {
  test.use(asUser('omer'))

  test('no Prod tasks; Stop but no Terminal on a Dev row', async ({ page }) => {
    await page.goto('/tasks?filter=running&group=0')
    const reportWorker = page.getByRole('row').filter({ hasText: 'report-worker' })
    await expect(reportWorker.first()).toBeVisible()
    await expect(reportWorker.first().getByRole('button', { name: 'Stop' })).toBeVisible()
    await expect(reportWorker.first().getByRole('link', { name: 'Terminal' })).toHaveCount(0)
    await expect(page.getByRole('link', { name: 'Terminal' })).toHaveCount(0)

    // Everyone sees the note; only admins get the link to change it.
    await expect(page.getByText('Stopped tasks older than 10 min are hidden.')).toBeVisible()
    await expect(page.getByRole('link', { name: 'Change', exact: true })).toHaveCount(0)

    await expect(page.getByRole('row').filter({ hasText: 'core-services' })).toHaveCount(0)
    await expect(page.getByRole('row').filter({ hasText: 'payments-api' })).toHaveCount(0)
  })
})
