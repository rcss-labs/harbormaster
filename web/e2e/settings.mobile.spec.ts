import type { Page } from '@playwright/test'
import { expectPhoneLayoutClean, expectSheetWithinViewport, watchPage } from './fixtures/phone'
import { asUser, expect, test } from './fixtures/test'

// Read-only phone checks of Settings and My access. Anything that saves lives in settings.mobile-flows.spec.ts.
test.use({ ...asUser('sam'), viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true, deviceScaleFactor: 3 })

const TABS = ['general', 'accounts', 'users', 'tags', 'sso', 'audit'] as const

async function open(page: Page, path: string) {
  watchPage(page)
  await page.goto(path)
  await expect(page.getByRole('main')).toBeVisible()
  await expect(page.getByRole('main').getByRole('heading', { level: 2 }).first()).toBeVisible()
  await page.waitForLoadState('networkidle')
}

for (const tab of TABS) {
  test(`/settings/${tab} is clean at 390`, async ({ page }) => {
    await open(page, `/settings/${tab}`)
    await expectPhoneLayoutClean(page)
  })
}

test('the section strip has an All sections sheet listing every settings tab', async ({ page }) => {
  await open(page, '/settings/general')
  await page.getByRole('button', { name: 'All sections' }).click()
  const sheet = page.getByRole('menu', { name: 'All sections' })
  await expect(sheet).toBeVisible()
  for (const label of ['General', 'AWS accounts', 'Users & permissions', 'Tags', 'Single sign-on', 'Audit log']) {
    await expect(sheet.getByRole('menuitem', { name: label })).toBeVisible()
  }
  await expectSheetWithinViewport(page)
  await page.keyboard.press('Escape')
})

test('users: actions sheet, per-tag permissions sheet and the preset sheet fit the screen', async ({ page }) => {
  await open(page, '/settings/users')
  await page.getByRole('button', { name: 'Actions for omer' }).click()
  await expect(page.getByRole('menu', { name: 'Actions for omer' })).toBeVisible()
  await expectSheetWithinViewport(page)
  await expectPhoneLayoutClean(page)

  await page.getByRole('menuitem', { name: 'Edit permissions' }).click()
  const perms = page.getByRole('dialog', { name: 'Edit permissions for omer' })
  await expect(perms.getByRole('button', { name: 'omer on Dev: Operator' })).toBeVisible()
  await expect(perms.getByRole('button', { name: 'omer on Staging: Viewer' })).toBeVisible()
  await expect(perms.getByRole('button', { name: 'omer on Prod: None' })).toBeVisible()
  await expectSheetWithinViewport(page)
  await expectPhoneLayoutClean(page)

  await perms.getByRole('button', { name: 'omer on Prod: None' }).click()
  const editor = page.getByRole('dialog', { name: 'Permissions for omer on Prod' })
  await expect(editor.getByLabel('Preset for omer on Prod')).toBeVisible()
  for (const action of ['View', 'Logs', 'Operate', 'Deploy', 'Edit task definition', 'Terminal']) {
    await expect(editor.getByLabel(action)).toBeVisible()
  }
  await expectSheetWithinViewport(page)
  await expectPhoneLayoutClean(page)
})

test('users: a disabled action shows its reason inside the sheet row', async ({ page }) => {
  await open(page, '/settings/users')
  await page.getByRole('button', { name: 'Actions for sam' }).click()
  const remove = page.getByRole('menuitem', { name: /^Delete/ })
  await expect(remove).toBeDisabled()
  await expect(remove).toContainText('You cannot delete yourself')
  await expectSheetWithinViewport(page)
})

test('users: Add user opens a sheet', async ({ page }) => {
  await open(page, '/settings/users')
  await page.getByRole('button', { name: 'Add user' }).click()
  await expect(page.getByRole('dialog').getByLabel('Username')).toBeVisible()
  await expectSheetWithinViewport(page)
  await expectPhoneLayoutClean(page)
})

test('tags: action sheet, create sheet and expanded assignments fit the screen', async ({ page }) => {
  await open(page, '/settings/tags')
  await page.getByRole('button', { name: 'Actions for tag Dev' }).click()
  const rename = page.getByRole('menuitem', { name: /^Rename/ })
  await expect(rename).toBeDisabled()
  await expect(rename).toContainText('Dev is the default tag')
  await expectSheetWithinViewport(page)
  await page.keyboard.press('Escape')

  await page.getByRole('button', { name: 'Create tag' }).first().click()
  await expect(page.getByRole('dialog', { name: 'Create tag' })).toBeVisible()
  await expectSheetWithinViewport(page)
  await expectPhoneLayoutClean(page)
  await page.keyboard.press('Escape')

  await page.getByRole('button', { name: 'Expand all' }).click()
  await expect(page.getByLabel(/^Tag for /).first()).toBeVisible()
  await expectPhoneLayoutClean(page)
})

test('accounts: action sheet, details sheet and connect sheet fit the screen', async ({ page }) => {
  await open(page, '/settings/accounts')
  await page.getByRole('button', { name: /^Actions for / }).first().click()
  const menu = page.getByRole('menu', { name: /^Actions for / })
  await expect(menu.getByRole('menuitem', { name: 'Test now' })).toBeVisible()
  await expectSheetWithinViewport(page)
  await menu.getByRole('menuitem', { name: 'Details' }).click()
  await expect(page.getByRole('dialog', { name: /^Details for / })).toBeVisible()
  await expectSheetWithinViewport(page)
  await expectPhoneLayoutClean(page)
  await page.keyboard.press('Escape')

  await page.getByRole('button', { name: 'Connect account' }).click()
  await expect(page.getByRole('dialog').getByLabel('Role ARN')).toBeVisible()
  await expectSheetWithinViewport(page)
  await expectPhoneLayoutClean(page)
})

test('audit: type chips and text filter work and stay inside the screen', async ({ page }) => {
  await open(page, '/settings/audit')
  await expect(page.getByRole('group', { name: 'Type' })).toBeVisible()
  await page.getByLabel('Filter audit log').fill('zzz-no-such-entry')
  await expect(page.getByText('No entries match.')).toBeVisible()
  await expect(page.getByRole('button', { name: 'Clear filters' })).toBeVisible()
  await expectPhoneLayoutClean(page)
})

test('sso: the checklist and the five panels are reachable', async ({ page }) => {
  await open(page, '/settings/sso')
  for (const name of ['Set up single sign-on', 'Identity provider', 'Sign-in options']) {
    await expect(page.getByRole('heading', { level: 2, name })).toBeVisible()
  }
  await expectPhoneLayoutClean(page)
})

test('my access: one panel per tag for an admin note and for omer', async ({ page, browser }) => {
  await open(page, '/me/access')
  await expect(page.getByText('You are a global admin: you can do everything, on every tag.')).toBeVisible()
  await expectPhoneLayoutClean(page)

  const context = await browser.newContext({ ...asUser('omer'), viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true, deviceScaleFactor: 3 })
  try {
    const omer = await context.newPage()
    watchPage(omer)
    await omer.goto('/me/access')
    await expect(omer.getByRole('region', { name: 'Permissions on Dev' })).toBeVisible()
    await expect(omer.getByRole('region', { name: 'Permissions on Staging' })).toBeVisible()
    await expect(omer.getByRole('region', { name: 'Permissions on Prod' })).toHaveCount(0)
    await omer.waitForLoadState('networkidle')
    await expectPhoneLayoutClean(omer)
  } finally {
    await context.close()
  }
})
