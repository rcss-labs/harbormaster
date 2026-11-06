import { SVC, serviceUrl } from './fixtures/ids'
import { expectPhoneLayoutClean, expectSheetWithinViewport, watchPage } from './fixtures/phone'
import { asUser, expect, test } from './fixtures/test'

// Read-only phone checks of the Tasks page and the service Tasks tab at 390 x 844 (chromium-mobile).
test.use({ viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true, deviceScaleFactor: 3 })

const rowTrigger = (page: import('@playwright/test').Page, hasText: string) =>
  page
    .getByRole('row')
    .filter({ hasText })
    .first()
    .getByRole('button', { name: /^Actions for task / })

test.describe('tasks as sam on a phone', () => {
  test.use(asUser('sam'))

  test('the lists are clean in every state and the row sheet and details sheet fit', async ({ page }) => {
    watchPage(page)
    for (const url of ['/tasks', '/tasks?filter=running&group=0', '/tasks?filter=stopped&group=0', '/tasks?filter=all']) {
      await page.goto(url)
      await expect(page.getByRole('group', { name: 'Task filter' })).toBeVisible()
      await expect(page.getByRole('row').first()).toBeVisible()
      await expectPhoneLayoutClean(page)
    }

    await page.goto('/tasks?filter=running&group=0')
    await rowTrigger(page, 'payments-api').click()
    const menu = page.getByRole('menu')
    await expect(menu.getByRole('menuitem', { name: 'Terminal' })).toBeVisible()
    await expect(menu.getByRole('menuitem', { name: 'Stop task' })).toBeVisible()
    await expectSheetWithinViewport(page)

    await menu.getByRole('menuitem', { name: 'Details' }).click()
    const details = page.getByRole('dialog')
    await expect(details.getByText('Identity')).toBeVisible()
    await expect(details.getByText('Recent log lines')).toBeVisible()
    await expectSheetWithinViewport(page)
    // The layout pass reads the page under the sheet, so it runs once the sheet is closed again.
    await details.getByRole('button', { name: 'Close panel' }).click()
    await expect(details).toHaveCount(0)
    await expectPhoneLayoutClean(page)
  })

  test('the filters sheet fits, narrows the list by service and clears', async ({ page }) => {
    watchPage(page)
    await page.goto('/tasks?filter=running&group=0')
    await expect(page.getByRole('row').first()).toBeVisible()
    const before = await page.getByRole('row').count()
    await page.getByRole('button', { name: 'Filters' }).click()
    const sheet = page.getByRole('dialog', { name: 'Filters and fields' })
    await expectSheetWithinViewport(page)
    await sheet.getByRole('combobox', { name: 'Service' }).selectOption('payments-api')
    await sheet.getByRole('button', { name: 'Done' }).click()
    await expect(page.getByText('Filters: Service payments-api')).toBeVisible()
    await expect.poll(() => page.getByRole('row').count()).toBeLessThan(before)
    await expectPhoneLayoutClean(page)
    await page.getByRole('button', { name: 'Clear filters' }).click()
    // Clear filters also resets the state chip to All, so the list can only grow.
    await expect.poll(() => page.getByRole('row').count()).toBeGreaterThanOrEqual(before)
    await expect(page.getByText(/^Filters: /)).toHaveCount(0)
  })

  test('an empty result keeps the page clean and offers Clear filters', async ({ page }) => {
    watchPage(page)
    await page.goto('/tasks?filter=all&q=no-such-task-anywhere')
    await expect(page.getByText('No tasks match.')).toBeVisible()
    await expect(page.getByRole('button', { name: 'Clear filters' })).toBeVisible()
    await expectPhoneLayoutClean(page)
  })

  test('the service Tasks tab shows rows with state chips', async ({ page }) => {
    watchPage(page)
    await page.goto(serviceUrl(...SVC.paymentsApi, 'tasks'))
    await expect(page.getByRole('group', { name: 'Task state' })).toBeVisible()
    await expect(page.getByRole('row').first()).toBeVisible()
    await rowTrigger(page, '').click()
    await expectSheetWithinViewport(page)
    await page.keyboard.press('Escape')
    await expect(page.getByRole('menu')).toHaveCount(0)
    // The page-wide layout pass is not run here: the service header's Deploy and AWS console links belong to the service page lane.
  })
})

test.describe('tasks as omer on a phone', () => {
  test.use(asUser('omer'))

  test('task sheet hides Terminal without the permission', async ({ page }) => {
    watchPage(page)
    await page.goto('/tasks?filter=running&group=0')
    const trigger = rowTrigger(page, 'report-worker')
    await expect(trigger).toBeVisible()
    await trigger.click()
    const menu = page.getByRole('menu')
    await expect(menu.getByRole('menuitem', { name: 'Stop task' })).toBeVisible()
    await expect(menu.getByRole('menuitem', { name: /Terminal/ })).toHaveCount(0)
    await expectSheetWithinViewport(page)
    await page.keyboard.press('Escape')
    await expect(menu).toHaveCount(0)
    await expect(page.getByRole('row').filter({ hasText: 'payments-api' })).toHaveCount(0)
    await expectPhoneLayoutClean(page)
  })
})
