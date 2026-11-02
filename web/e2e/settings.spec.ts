import { asUser, expect, test } from './fixtures/test'

// Mutating (it changes the global stopped-tasks window and restores it): runs in the flows project.
const windowInput = (page: import('@playwright/test').Page) => page.getByLabel('Show stopped tasks from the last X minutes')

test.describe('settings as pavel', () => {
  test.use(asUser('pavel'))

  test('sidebar Settings opens General with the window card', async ({ page }) => {
    await page.goto('/')
    await page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'Settings' }).click()
    await expect(page).toHaveURL(/\/settings\/general$/)
    await expect(page.getByRole('heading', { level: 1, name: 'Settings' })).toBeVisible()
    await expect(page.getByRole('heading', { level: 2, name: 'Tags' })).toHaveCount(0)
    await expect(windowInput(page)).toHaveValue('10')
  })

  test('tabs and redirects', async ({ page }) => {
    await page.goto('/settings/general')
    await page.getByRole('link', { name: 'AWS accounts' }).click()
    await expect(page).toHaveURL(/\/settings\/accounts$/)
    await expect(page.getByRole('heading', { level: 2, name: 'AWS accounts' })).toBeVisible()

    await page.getByRole('link', { name: 'Tags', exact: true }).click()
    await expect(page).toHaveURL(/\/settings\/tags$/)
    await expect(page.getByRole('heading', { level: 2, name: 'Tags' })).toBeVisible()

    await page.goto('/admin/tags')
    await expect(page).toHaveURL(/\/settings\/tags$/)
    await page.goto('/admin/audit')
    await expect(page).toHaveURL(/\/settings\/audit$/)
    await page.goto('/settings/bogus')
    await expect(page).toHaveURL(/\/settings\/general$/)
  })

  test('changing the window hides and reveals stopped tasks, is audited, and is restored', async ({ page }) => {
    await page.goto('/settings/general')
    try {
      await windowInput(page).fill('90')
      await page.getByRole('button', { name: 'Save' }).click()
      await expect(page.getByText('Saved.')).toBeVisible()

      await page.goto('/tasks?filter=stopped&group=0')
      const stoppedResizer = page.getByRole('row').filter({ hasText: 'image-resizer' }).filter({ hasText: 'Stopped (exit 137)' })
      // The seed holds six stops (4 to 64 min ago); change.spec may have added more.
      await expect.poll(() => stoppedResizer.count()).toBeGreaterThanOrEqual(6)
      await expect(page.getByText('Stopped tasks older than 90 min are hidden.')).toBeVisible()

      await page.goto('/settings/audit')
      await expect(page.getByText('Changed the stopped tasks window from 10 to 90 minutes.').first()).toBeVisible()
    } finally {
      try {
        await page.goto('/settings/general')
        await windowInput(page).fill('10')
        await page.getByRole('button', { name: 'Save' }).click()
        await expect(page.getByText('Saved.')).toBeVisible()
      } finally {
        // Independent of the UI: never leave the global window changed for later specs.
        await page.request.put('/api/settings', { data: { stopped_tasks_window_minutes: 10 } })
      }
      await expect
        .poll(async () => (await (await page.request.get('/api/settings')).json()).stopped_tasks_window_minutes)
        .toBe(10)
    }
  })
})

test.describe('settings as omer', () => {
  test.use(asUser('omer'))

  test('settings is admin-only', async ({ page }) => {
    await page.goto('/settings/general')
    await expect(page.getByRole('heading', { name: 'No access' })).toBeVisible()
    const response = await page.request.put('/api/settings', { data: { stopped_tasks_window_minutes: 5 } })
    expect(response.status()).toBe(403)
  })
})
