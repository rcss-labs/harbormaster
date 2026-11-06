import { asUser, expect, test } from './fixtures/test'
import { SVC, serviceUrl } from './fixtures/ids'

// Read-only: runs in the observe project (the `observe.spec.ts` pattern matches this name), before change.spec's
// OOM rollback gives image-resizer running :6 tasks. On the seed it has 0 of 2 running.

test.describe('tasks as omer', () => {
  test.use(asUser('omer'))

  test('image-resizer: OOM-killed stopped tasks, none running, no Terminal button', async ({ page }) => {
    await page.goto(serviceUrl(...SVC.imageResizer, 'tasks'))
    const stopped = page.getByRole('row').filter({ hasText: 'Stopped (exit 137)' })
    await expect(stopped.first()).toBeVisible()
    await expect(stopped.first()).toContainText('OutOfMemoryError')
    await expect(page.getByRole('row').getByText('Running', { exact: true })).toHaveCount(0)
    // The tab bar also has a Terminal link (locked for omer), so look inside the task table only.
    await expect(page.getByRole('table').getByRole('link', { name: 'Terminal' })).toHaveCount(0)
  })

  test('a stopped task opens the drawer with the raw ECS message and no Stop', async ({ page }) => {
    await page.goto(serviceUrl(...SVC.imageResizer, 'tasks'))
    const stopped = page.getByRole('row').filter({ hasText: 'Stopped (exit 137)' }).first()
    await stopped.getByRole('button', { name: /^Open details for task / }).click()
    const drawer = page.getByRole('dialog')
    await expect(drawer.getByText('OutOfMemoryError').first()).toBeVisible()
    await expect(drawer.getByRole('button', { name: 'Stop task' })).toHaveCount(0)
    await page.keyboard.press('Escape')
    await expect(drawer).toHaveCount(0)
  })
})
