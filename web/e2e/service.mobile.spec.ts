import type { Page } from '@playwright/test'
import { SVC, serviceUrl } from './fixtures/ids'
import { expectPhoneLayoutClean, expectSheetWithinViewport, watchPage } from './fixtures/phone'
import { asUser, expect, test } from './fixtures/test'
import { measureBox } from './helpers'

// Read-only phone checks of the service page frame and the Overview, Metrics, Scaling, Task definition and History tabs.
test.use({ ...asUser('sam'), viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true, deviceScaleFactor: 3 })

const paymentsApi = (tab?: string) => serviceUrl(...SVC.paymentsApi, tab)

async function open(page: Page, tab: string) {
  watchPage(page)
  await page.goto(paymentsApi(tab))
  await expect(page.getByRole('heading', { name: 'payments-api', level: 1 })).toBeAttached()
  await page.waitForLoadState('networkidle')
}

for (const tab of ['overview', 'metrics', 'scaling', 'task-definition', 'operations']) {
  test(`${tab} is laid out cleanly at 390`, async ({ page }) => {
    await open(page, tab)
    await expectPhoneLayoutClean(page)
  })
}

test('the compact header shows Deploy and the actions sheet keeps every row', async ({ page }) => {
  await open(page, 'overview')
  await expect(page.getByRole('link', { name: 'Deploy', exact: true }).first()).toBeVisible()
  await page.getByRole('button', { name: 'More actions for payments-api' }).click()
  const menu = page.getByRole('menu', { name: 'More actions for payments-api' })
  for (const name of [/^Restart/, /^Change desired count/, /^Deploy/, /^Pin to Overview and Services/, /^AWS console/]) {
    await expect(menu.getByRole('menuitem', { name })).toBeVisible()
  }
  await expectSheetWithinViewport(page)
  await expectPhoneLayoutClean(page)
  await page.keyboard.press('Escape')
  await expect(menu).toBeHidden()
})

test('Change desired count opens a sheet and then the confirmation without applying', async ({ page }) => {
  await open(page, 'overview')
  await page.getByRole('button', { name: 'More actions for payments-api' }).click()
  await page.getByRole('menuitem', { name: /^Change desired count/ }).click()
  const sheet = page.getByRole('dialog', { name: 'Change desired count' })
  await expect(sheet).toBeVisible()
  await sheet.getByRole('button', { name: 'Increase desired count' }).click()
  await sheet.getByRole('button', { name: 'Update desired count' }).click()
  const confirm = page.getByRole('dialog', { name: /^Set payments-api to \d+ tasks\?$/ })
  await expect(confirm).toBeVisible()
  await expectSheetWithinViewport(page)
  await expectPhoneLayoutClean(page)
  await confirm.getByRole('button', { name: 'Cancel' }).click()
  await expect(confirm).toBeHidden()
})

test('every section is reachable: All sections lists nine and the strip scrolls the open tab into view', async ({ page }) => {
  await open(page, 'operations')
  await page.getByRole('button', { name: 'All sections' }).click()
  const menu = page.getByRole('menu', { name: 'All sections' })
  await expect(menu.getByRole('menuitem')).toHaveCount(9)
  for (const name of ['Overview', 'Tasks', 'Logs', 'Metrics', 'Terminal', 'Deploy', 'Task definition', 'Scaling', 'History']) {
    await expect(menu.getByRole('menuitem', { name: new RegExp(`^${name}`) })).toBeVisible()
  }
  await expectSheetWithinViewport(page)
  await menu.getByRole('menuitem', { name: /^Task definition/ }).click()
  await expect(page).toHaveURL(/\/task-definition$/)

  // History is the last tab: with it open, it sits inside the strip's visible width, not behind the fade.
  await page.goto(paymentsApi('operations'))
  const history = page.getByRole('navigation', { name: 'Service tabs' }).getByRole('link', { name: 'History' })
  await expect(history).toBeVisible()
  const nav = await measureBox(page.getByRole('navigation', { name: 'Service tabs' }))
  const tab = await measureBox(history)
  expect(tab.x).toBeGreaterThanOrEqual(nav.x - 1)
  expect(tab.x + tab.width).toBeLessThanOrEqual(nav.x + nav.width + 1)
})

test('Metrics range chips switch the range', async ({ page }) => {
  await open(page, 'metrics')
  await page.getByRole('group', { name: 'Range' }).getByRole('button', { name: '6h' }).click()
  await expect(page).toHaveURL(/range=6h/)
  await expectPhoneLayoutClean(page)
})

test('Overview rollout panel and the task actions sheet', async ({ page }) => {
  await open(page, 'overview')
  const rollout = page.getByRole('region', { name: 'Rollout' })
  await expect(rollout.getByRole('button', { name: 'Restart' })).toBeVisible()
  await expect(rollout.getByRole('link', { name: 'Open Deploy' })).toBeVisible()
  await page
    .getByRole('button', { name: /^Actions for task / })
    .first()
    .click()
  await expect(page.getByRole('menu', { name: /^Actions for task / })).toBeVisible()
  await expectSheetWithinViewport(page)
  await expectPhoneLayoutClean(page)
})

test('Task definition: editing shows the sticky bar, the review sheet fits and revisions open a sheet', async ({ page }) => {
  await open(page, 'task-definition')
  const editor = page.getByRole('textbox', { name: 'Task definition JSON' })
  // A real change: bump the cpu value.
  const text = await editor.inputValue()
  await editor.fill(text.replace(/"cpu": "?(\d+)"?/, (_m, cpu) => `"cpu": "${Number(cpu) + 1}"`))
  const bar = page.getByRole('region', { name: 'Pending changes' })
  await expect(bar).toBeVisible()
  await expectPhoneLayoutClean(page)
  await bar.getByRole('button', { name: /^Review and register/ }).click()
  await expect(page.getByRole('dialog', { name: /^Register payments-api:\d+\?$/ })).toBeVisible()
  await expectSheetWithinViewport(page)
  await expectPhoneLayoutClean(page)
  await page.getByRole('button', { name: 'Keep editing' }).click()
  await bar.getByRole('button', { name: 'Discard' }).click()

  await page
    .getByRole('button', { name: /^Actions for revision / })
    .first()
    .click()
  await expect(page.getByRole('menu', { name: /^Actions for revision / })).toBeVisible()
  await expectSheetWithinViewport(page)
})

test('History: toggle, notes and feed fit', async ({ page }) => {
  await open(page, 'operations')
  await expect(page.getByLabel('Show ECS service events')).toBeVisible()
  await expect(page.getByText('Actions taken in Harbormaster are kept for 90 days.', { exact: false })).toBeVisible()
  await expectPhoneLayoutClean(page)
})
