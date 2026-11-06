import type { Page } from '@playwright/test'
import { asUser, expect, test } from './fixtures/test'
import { expectPhoneLayoutClean, expectSheetWithinViewport, watchPage } from './fixtures/phone'
import { SVC, serviceUrl } from './fixtures/ids'
import { measureBox } from './helpers'

// Read-only phone checks of the Deploy tab: nothing here applies a change (that is deploy.mobile-flows.spec.ts).
test.use({ ...asUser('sam'), viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true, deviceScaleFactor: 3 })

const deployUrl = serviceUrl(...SVC.paymentsApi, 'deploy')
const bar = (page: Page) => page.getByRole('region', { name: 'Pending changes' })

async function openDeploy(page: Page) {
  watchPage(page)
  await page.goto(deployUrl)
  await expect(page.getByRole('region', { name: 'Service' }).getByRole('spinbutton', { name: 'Desired count' })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Add variable' })).toBeVisible()
}

test('Deploy lays out cleanly at 390 with no bar until something changes', async ({ page }) => {
  await openDeploy(page)
  await expect(bar(page)).toHaveCount(0)
  await expectPhoneLayoutClean(page)
})

test('the sticky bar shows the count, never covers the last field, and its sheet fits the screen', async ({ page }) => {
  await openDeploy(page)
  await page.getByRole('region', { name: 'Service' }).getByRole('spinbutton', { name: 'Desired count' }).fill('4')
  await expect(bar(page).getByText('1 change')).toBeVisible()
  await expect(page.locator('html')).toHaveAttribute('data-hm-bar', '')
  await expectPhoneLayoutClean(page)

  // Scrolled to the end, the last control of the page sits above the bar.
  await page.locator('main').evaluate((main) => (main.scrollTop = main.scrollHeight))
  const lastControl = page.locator('main').locator('button, input, select, textarea').last()
  await lastControl.scrollIntoViewIfNeeded()
  const [barBox, lastBox] = await Promise.all([measureBox(bar(page)), measureBox(lastControl)])
  expect(lastBox.y + lastBox.height).toBeLessThanOrEqual(barBox.y + 1)

  await bar(page).getByRole('button', { name: 'Pending changes: 1 change' }).click()
  const sheet = page.getByRole('dialog', { name: 'Pending changes' })
  await expect(sheet.getByText('Desired count')).toBeVisible()
  await expectSheetWithinViewport(page)
  await page.keyboard.press('Escape')

  await bar(page).getByRole('button', { name: 'Review…' }).click()
  await expect(page.getByRole('dialog', { name: 'Review changes to payments-api' })).toBeVisible()
  await expectSheetWithinViewport(page)
  await page.getByRole('button', { name: 'Keep editing' }).click()

  await bar(page).getByRole('button', { name: 'Discard' }).click()
  await expect(bar(page)).toHaveCount(0)
  await expect(page.locator('html')).not.toHaveAttribute('data-hm-bar', '')
})

test('the image tag search opens in a sheet and a policy row opens its Edit/Remove sheet', async ({ page }) => {
  await openDeploy(page)
  await page.getByRole('combobox', { name: 'Image tag' }).click()
  const tags = page.getByRole('dialog', { name: 'Image tag' })
  await expect(tags.getByRole('searchbox', { name: 'Search image tags' })).toBeVisible()
  await expect(tags.getByRole('option').first()).toBeVisible()
  await expectSheetWithinViewport(page)
  await page.keyboard.press('Escape')
  await expect(tags).toBeHidden()

  await page.getByRole('button', { name: /^Actions for policy / }).first().click()
  const menu = page.getByRole('menu')
  await expect(menu.getByRole('menuitem', { name: /^Edit/ })).toBeVisible()
  await expectSheetWithinViewport(page)
  await menu.getByRole('menuitem', { name: /^Edit/ }).click()
  await expect(page.getByRole('button', { name: 'Save policy' })).toBeVisible()
  await expectSheetWithinViewport(page)
  await page.getByRole('button', { name: 'Cancel' }).click()
  await expectPhoneLayoutClean(page)
})
