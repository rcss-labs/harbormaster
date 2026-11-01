import { asUser, expect, test } from './fixtures/test'

// Read-only: runs in the observe project, before change.spec touches image-resizer.

test.describe('tasks as pavel', () => {
  test.use(asUser('pavel'))

  test('Problems first lists the image-resizer task stopped 4 min ago and hides the older ones', async ({ page }) => {
    await page.goto('/tasks')
    const stoppedResizer = page.getByRole('row').filter({ hasText: 'image-resizer' }).filter({ hasText: 'Stopped (exit 137)' })
    await expect(stoppedResizer.first()).toContainText('OutOfMemoryError')
    await expect.poll(() => stoppedResizer.count()).toBeGreaterThanOrEqual(1)
    // Only the 4-minute stop is inside the default 10-minute window; the 16 to 64 minute stops are hidden.
    // One auto-retrying check over a snapshot: rows can drop out of the window mid-check. The 4-minute stop ages out
    // about 6 minutes after seeding, so the observe phase must run within roughly 6 minutes of the server starting.
    await expect
      .poll(async () => (await stoppedResizer.allTextContents()).every((text) => /\b[0-9] min ago|just now|\d+s ago/.test(text)))
      .toBe(true)
    await expect(page.getByText('Stopped tasks older than 10 min are hidden.')).toBeVisible()
    await expect(page.getByRole('link', { name: 'Change', exact: true })).toBeVisible()

    await page.getByRole('group', { name: 'Task filter' }).getByRole('button', { name: /^Running/ }).click()
    await expect(page).toHaveURL(/filter=running/)
    await expect(page.getByRole('row').filter({ hasText: 'Running' }).first()).toBeVisible()
    await expect(page.getByRole('row').filter({ hasText: 'Stopped (exit 137)' })).toHaveCount(0)
  })
})

test.describe('tasks as omer', () => {
  test.use(asUser('omer'))

  test('no Prod tasks; Stop but no Terminal on a Dev row', async ({ page }) => {
    await page.goto('/tasks?filter=running')
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
