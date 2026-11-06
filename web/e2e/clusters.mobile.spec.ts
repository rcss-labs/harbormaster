import { asUser, expect, test } from './fixtures/test'
import { expectPhoneLayoutClean, expectSheetWithinViewport, watchPage } from './fixtures/phone'

// Read-only phone pass over the Clusters list and a cluster detail, as the demo admin. Runs in chromium-mobile.

test.describe('clusters on a phone as sam', () => {
  test.use(asUser('sam'))

  test('list: chips, rows, the row sheet and the sort sheet fit the screen', async ({ page }) => {
    watchPage(page)
    await page.goto('/clusters')
    await expect(page.getByRole('table', { name: 'Clusters' })).toBeVisible()
    await expect(page.getByRole('group', { name: 'Tag' })).toBeVisible()
    await expect(page.getByRole('group', { name: 'Status' })).toBeVisible()
    await expect(page.getByRole('link', { name: 'core-services' }).first()).toBeVisible()
    await expectPhoneLayoutClean(page)

    await page.getByRole('button', { name: 'Actions for core-services' }).click()
    const menu = page.getByRole('menu', { name: 'Actions for core-services' })
    await expect(menu).toBeVisible()
    await expect(menu.getByRole('menuitem', { name: /Open cluster/ })).toBeVisible()
    await expectSheetWithinViewport(page)
    await expectPhoneLayoutClean(page)
    await page.keyboard.press('Escape')
    await expect(menu).toBeHidden()

    await page.getByRole('button', { name: 'Sort and fields' }).click()
    await expect(page.getByRole('dialog', { name: 'Sort and fields' })).toBeVisible()
    await expectSheetWithinViewport(page)
    await page.keyboard.press('Escape')
    await expectPhoneLayoutClean(page)
  })

  test('detail: the status row, section chips and the More actions sheet', async ({ page }) => {
    watchPage(page)
    await page.goto('/clusters')
    await page.getByRole('link', { name: 'core-services' }).first().click()
    await expect(page.getByRole('heading', { level: 1, name: 'core-services' })).toBeVisible()
    await expect(page.getByRole('group', { name: 'Section' })).toBeVisible()
    await expectPhoneLayoutClean(page)

    await page.getByRole('group', { name: 'Section' }).getByRole('button', { name: 'Tasks' }).click()
    await expect(page.getByRole('heading', { level: 2, name: 'Tasks by service' })).toBeInViewport()
    await expectPhoneLayoutClean(page)

    await page.getByRole('button', { name: 'More actions for core-services' }).click()
    await expect(page.getByRole('menu', { name: 'Actions for core-services' })).toBeVisible()
    await expectSheetWithinViewport(page)
    await page.keyboard.press('Escape')
  })
})
