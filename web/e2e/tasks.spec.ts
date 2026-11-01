import { asUser, expect, test } from './fixtures/test'

// Read-only: runs in the observe project, before change.spec touches image-resizer.

test.describe('tasks as pavel', () => {
  test.use(asUser('pavel'))

  test('Problems first lists the OOM-killed image-resizer tasks; Running hides them', async ({ page }) => {
    await page.goto('/tasks')
    const stoppedResizer = page.getByRole('row').filter({ hasText: 'image-resizer' }).filter({ hasText: 'Stopped (exit 137)' })
    await expect(stoppedResizer.first()).toContainText('OutOfMemoryError')
    // The seed holds six; the fake keeps failing replacements while the service runs, so the count only grows.
    await expect.poll(() => stoppedResizer.count()).toBeGreaterThanOrEqual(6)

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

    await expect(page.getByRole('row').filter({ hasText: 'core-services' })).toHaveCount(0)
    await expect(page.getByRole('row').filter({ hasText: 'payments-api' })).toHaveCount(0)
  })
})
