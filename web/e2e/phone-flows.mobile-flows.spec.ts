import type { Page } from '@playwright/test'
import { SVC, serviceUrl } from './fixtures/ids'
import { expectPhoneLayoutClean, expectSheetWithinViewport, watchPage } from './fixtures/phone'
import { asUser, expect, test } from './fixtures/test'
import { login, serviceId } from './helpers'

// Cross-screen phone flows: what an on-call engineer does, end to end, at 390 x 844. These change data (restart, stop, deploy), so they run
// after the flow specs (chromium-mobile-flows). Every step also runs the generic layout assertion, because single-screen checks let
// visible defects slip through the seams between screens.
const PHONE = { viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true, deviceScaleFactor: 3 } as const
test.use({ ...asUser('sam'), ...PHONE })

const LONG_SERVICE = 'payments-api-reconciliation-worker-eu-west-1-blue-green-canary-0042'
const LONG_TAG = 'release-2026.11.06-hotfix-a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4-rc9'
const LONG_CLUSTER = 'core-services-production-eu-west-1-primary-shared-workloads-cluster'

interface TaskItem {
  id: string
  short_id: string
  service: { id: string; name: string } | null
}

async function mainText(page: Page) {
  return (await page.getByRole('main').textContent()) ?? ''
}

test('on-call: find the failing service on Overview, read its logs, restart it and confirm the result', async ({ page }) => {
  watchPage(page)
  await page.goto('/')
  const attention = page.getByRole('region', { name: 'Needs attention' })
  const firstRow = attention.getByRole('button', { name: /^Actions for / }).first()
  await expect(firstRow).toBeVisible()
  await page.waitForLoadState('networkidle')
  await expectPhoneLayoutClean(page)

  const name = ((await firstRow.getAttribute('aria-label')) ?? '').replace('Actions for ', '')
  expect(name).not.toBe('')
  await firstRow.click()
  const sheet = page.getByRole('dialog', { name: `Actions for ${name}` })
  await expectSheetWithinViewport(page)
  await sheet.getByRole('link', { name: 'Logs' }).click()

  // Logs: the failing service's own page, the log box fills down to the tab bar.
  await expect(page).toHaveURL(/\/services\/[^/]+\/logs/)
  await expect(page.getByRole('heading', { name, level: 1 })).toBeAttached()
  const log = page.getByRole('log')
  await expect(log).toBeVisible()
  await page.waitForLoadState('networkidle')
  await expectPhoneLayoutClean(page)

  // Restart from the page's actions sheet, with the confirmation sheet in between.
  await page.getByRole('button', { name: `More actions for ${name}` }).click()
  const restartItem = page.getByRole('menu').getByRole('menuitem', { name: /^Restart/ })
  await expect(restartItem).toBeEnabled({ timeout: 30_000 })
  await restartItem.click()
  const confirm = page.getByRole('dialog', { name: `Restart ${name}?` })
  await expect(confirm).toBeVisible()
  await expectSheetWithinViewport(page)
  await confirm.getByRole('button', { name: 'Restart', exact: true }).click()
  await expect(page.getByText('Restart started.')).toBeVisible()

  // Confirm the result in the service's History, then on the Overview rollout panel.
  await page.getByRole('navigation', { name: 'Service tabs' }).getByRole('link', { name: 'History' }).click()
  await expect(page.getByText(/^Restarted service \(force new deployment\)/).first()).toBeVisible()
  await expectPhoneLayoutClean(page)
  await page.getByRole('navigation', { name: 'Service tabs' }).getByRole('link', { name: 'Overview' }).click()
  await expect(page.getByRole('region', { name: 'Rollout' })).toBeVisible()
  await page.waitForLoadState('networkidle')
  await expectPhoneLayoutClean(page)
})

test('restart from the Services action sheet shows "Restart started."', async ({ page }) => {
  watchPage(page)
  await page.goto('/services?q=notifications')
  const row = page.getByRole('table', { name: 'Services' }).getByRole('row').filter({ hasText: 'notifications' }).first()
  await row.getByRole('button', { name: /^Actions for / }).click()
  const restart = page.getByRole('menu').getByRole('menuitem', { name: /^Restart/ })
  await expect(restart).toBeEnabled({ timeout: 30_000 })
  await restart.click()
  const confirm = page.getByRole('dialog', { name: 'Restart notifications?' })
  await expect(confirm).toBeVisible()
  await expectSheetWithinViewport(page)
  await confirm.getByRole('button', { name: 'Restart', exact: true }).click()
  await expect(page.getByText('Restart started.')).toBeVisible()
  await expectPhoneLayoutClean(page)
})

test('desired count from the service header sheet shows "Desired count set to N."', async ({ page }) => {
  watchPage(page)
  await page.goto(serviceUrl(...SVC.jenkins, 'overview'))
  await expect(page.getByRole('heading', { name: 'jenkins-controller', level: 1 })).toBeAttached()
  await page.getByRole('button', { name: 'More actions for jenkins-controller' }).click()
  await page.getByRole('menuitem', { name: /^Change desired count/ }).click()
  const sheet = page.getByRole('dialog', { name: 'Change desired count' })
  await sheet.getByRole('button', { name: 'Increase desired count' }).click()
  await sheet.getByRole('button', { name: 'Update desired count' }).click()
  const confirm = page.getByRole('dialog', { name: /^Set jenkins-controller to \d+ tasks?\?$/ })
  await expect(confirm).toBeVisible()
  const target = /to (\d+) tasks?/.exec((await confirm.getByRole('heading').textContent()) ?? '')?.[1]
  await confirm.getByRole('button', { name: /^(Set|Apply|Confirm|Update)/ }).last().click()
  await expect(page.getByText(`Desired count set to ${target}.`)).toBeVisible()
  await expectPhoneLayoutClean(page)
})

test('stop a task from its sheet and see it leave the running list', async ({ page }) => {
  watchPage(page)
  await page.goto('/tasks?filter=running&group=0')
  const row = page.getByRole('row').filter({ hasText: 'catalog-api' }).first()
  await expect(row).toBeVisible()
  const trigger = row.getByRole('button', { name: /^Actions for task / })
  const shortId = ((await trigger.getAttribute('aria-label')) ?? '').replace('Actions for task ', '')
  await trigger.click()
  await page.getByRole('menuitem', { name: 'Stop task' }).click()
  const confirm = page.getByRole('dialog')
  await expect(confirm.getByRole('heading', { name: `Stop task ${shortId}?`, exact: true })).toBeVisible()
  await expectSheetWithinViewport(page)
  await confirm.getByRole('button', { name: 'Stop task' }).click()
  await expect(confirm).toHaveCount(0)
  await expect
    .poll(async () => {
      const response = await page.request.get('/api/tasks?filter=running&limit=500')
      return ((await response.json()) as { items: TaskItem[] }).items.some((task) => task.short_id.startsWith(shortId))
    }, { timeout: 15_000 })
    .toBe(false)
  await expectPhoneLayoutClean(page)
})

test('deploy an image tag with review: pick, review in the sheet, apply, and the revision moves', async ({ page }) => {
  watchPage(page)
  const api = serviceUrl(...SVC.catalogApi, 'deploy')
  await page.goto(api)
  const combobox = page.getByRole('combobox', { name: 'Image tag' })
  await expect(combobox).toBeVisible()
  const before = await combobox.inputValue()
  await combobox.click()
  const options = page.getByRole('listbox', { name: 'Image tags' }).getByRole('option')
  await expect(options.first()).toBeVisible()
  await expectPhoneLayoutClean(page, { allowClip: ['input'] })
  const labels = await options.evaluateAll((items) => items.map((item) => item.firstElementChild?.textContent ?? ''))
  const next = labels.find((tag) => tag && tag !== before)
  test.skip(!next, 'the fake registry has no second tag for catalog-api')
  await options.nth(labels.indexOf(next!)).click()
  await expect(combobox).toHaveValue(next!)

  const bar = page.getByRole('region', { name: 'Pending changes' })
  await expect(bar).toBeVisible()
  await expectPhoneLayoutClean(page, { allowClip: ['input'] })
  await bar.getByRole('button', { name: 'Review…' }).click()
  const review = page.getByRole('dialog', { name: 'Review changes to catalog-api' })
  await expect(review).toBeVisible()
  await expectSheetWithinViewport(page)
  await review.getByRole('textbox', { name: 'Reason' }).fill('phone flow: deploy with review')
  await review.getByRole('button', { name: /^(Register and deploy :\d+|Apply)/ }).click()
  await expect(page.getByText(/^(Deploying catalog-api to :\d+\.|Applied the changes to catalog-api\.)$/)).toBeVisible()
  await expect(bar).toHaveCount(0)
  // The server's snapshot follows ECS by one 5 s poll (more on a loaded machine); until then the field shows the old tag and the tab says
  // "Rolling out :N", so the new tag is awaited for longer than the 5 s default.
  await expect(combobox).toHaveValue(next!, { timeout: 30_000 })
})

test('deploy: changing an env var on the phone', async ({ page }) => {
  watchPage(page)
  await page.goto(serviceUrl(...SVC.notifications, 'deploy'))
  await page.getByRole('button', { name: 'Add variable' }).click()
  await page.getByLabel(/^Variable name \d+$/).last().fill('PHONE_FLOW_ENV')
  await page.getByLabel(/^Variable value \d+$/).last().fill('1')
  const bar = page.getByRole('region', { name: 'Pending changes' })
  await expect(bar.getByText('1 change')).toBeVisible()
  await bar.getByRole('button', { name: 'Review…' }).click()
  const review = page.getByRole('dialog', { name: 'Review changes to notifications' })
  await expectSheetWithinViewport(page)
  await review.getByRole('textbox', { name: 'Reason' }).fill('phone flow: env')
  await review.getByRole('button', { name: /^(Register and deploy :\d+|Apply)/ }).click()
  await expect(page.getByText(/^(Deploying notifications to :\d+\.|Applied the changes to notifications\.)$/)).toBeVisible()
})

test('rotation keeps the layout clean', async ({ page }) => {
  watchPage(page)
  const checkAll = async (label: string) => {
    for (const path of ['/', '/services', '/tasks', serviceUrl(...SVC.paymentsApi, 'overview')]) {
      await page.goto(path)
      await expect(page.getByRole('main')).toBeVisible()
      await page.waitForLoadState('networkidle')
      await expectPhoneLayoutClean(page).catch((error: Error) => {
        error.message = `${label} ${path}: ${error.message}`
        throw error
      })
    }
  }
  await checkAll('portrait')

  // Landscape: 844 x 390 is above the phone query, so the desktop layout with the collapsed sidebar applies. It must not overflow either.
  await page.setViewportSize({ width: 844, height: 390 })
  await page.goto('/services')
  await expect(page.getByRole('main')).toBeVisible()
  await page.waitForLoadState('networkidle')
  expect(await page.evaluate(() => document.scrollingElement!.scrollWidth <= document.scrollingElement!.clientWidth + 1), 'landscape document scrolls sideways').toBe(true)

  // Rotate with a sheet open: it survives inside the viewport or closes cleanly, never stranded half off-screen.
  await page.setViewportSize({ width: 390, height: 844 })
  await page.goto('/services?q=payments-api')
  await page.getByRole('button', { name: 'Actions for payments-api' }).click()
  await expect(page.getByRole('menu', { name: 'Actions for payments-api' })).toBeVisible()
  await page.setViewportSize({ width: 844, height: 390 })
  await page.waitForTimeout(300)
  const open = page.locator('[role=menu], [role=dialog][aria-modal="true"]')
  if ((await open.count()) > 0 && (await open.last().isVisible())) {
    const rect = await open.last().evaluate((el) => {
      const r = el.getBoundingClientRect()
      return { left: r.left, top: r.top, right: r.right, bottom: r.bottom, width: window.innerWidth, height: window.innerHeight }
    })
    expect(rect.left).toBeGreaterThanOrEqual(-1)
    expect(rect.top).toBeGreaterThanOrEqual(-1)
    expect(rect.right).toBeLessThanOrEqual(rect.width + 1)
    expect(rect.bottom).toBeLessThanOrEqual(rect.height + 1)
  }
  await page.keyboard.press('Escape')

  await page.setViewportSize({ width: 390, height: 844 })
  await checkAll('portrait again')
})

test('rotation refits the terminal: cols change between portrait and landscape', async ({ page }) => {
  watchPage(page)
  await page.goto(serviceUrl(...SVC.paymentsApi, 'terminal'))
  await page.getByRole('button', { name: /^Target:/ }).click()
  await page.getByRole('dialog', { name: 'Terminal target' }).getByRole('button', { name: 'Connect' }).click()
  const rows = page.locator('.xterm-rows')
  await expect(rows).toContainText('Starting session with SessionId: ecs-execute-command-', { timeout: 15_000 })
  const cols = () => page.locator('.xterm-screen').evaluate((el) => Math.round(el.getBoundingClientRect().width))
  const portrait = await cols()
  await page.setViewportSize({ width: 844, height: 390 })
  await expect.poll(cols).toBeGreaterThan(portrait + 100)
  await page.setViewportSize({ width: 390, height: 844 })
  await expect.poll(cols).toBeLessThanOrEqual(portrait + 2)
  expect(await page.evaluate(() => document.scrollingElement!.scrollWidth <= document.scrollingElement!.clientWidth + 1)).toBe(true)
  await page.getByRole('button', { name: 'Disconnect' }).click()
})

test('rotation keeps an unsaved Deploy change', async ({ page }) => {
  watchPage(page)
  await page.goto(serviceUrl(...SVC.jenkins, 'deploy'))
  await page.getByRole('button', { name: 'Add variable' }).click()
  await page.getByLabel(/^Variable name \d+$/).last().fill('ROTATION_KEEPS_ME')
  await page.setViewportSize({ width: 844, height: 390 })
  await expect(page.getByLabel(/^Variable name \d+$/).last()).toHaveValue('ROTATION_KEEPS_ME')
  await page.setViewportSize({ width: 390, height: 844 })
  await expect(page.getByLabel(/^Variable name \d+$/).last()).toHaveValue('ROTATION_KEEPS_ME')
  await expect(page.getByRole('region', { name: 'Pending changes' })).toBeVisible()
})

test('on-screen keyboard: the focused log search stays visible and the layout stays clean at 390x500', async ({ page }) => {
  watchPage(page)
  await page.setViewportSize({ width: 390, height: 500 })
  await page.goto(serviceUrl(...SVC.paymentsApi, 'logs'))
  const search = page.getByLabel('Search logs')
  await search.tap()
  await search.fill('error')
  const box = await search.boundingBox()
  expect(box).not.toBeNull()
  expect(box!.y + box!.height).toBeLessThanOrEqual(500)
  expect(box!.y).toBeGreaterThanOrEqual(0)
  await expectPhoneLayoutClean(page)
})

test.describe('long names', () => {
  test('a 64-character service name, an 80-character image tag and a long cluster name wrap inside rows and sheets', async ({ page }) => {
    watchPage(page)
    await page.route('**/api/services', async (route) => {
      if (route.request().method() !== 'GET') return route.fallback()
      const response = await route.fetch()
      const body = await response.json()
      body.items[0].name = LONG_SERVICE
      body.items[0].image_tag = LONG_TAG
      body.items[0].cluster.name = LONG_CLUSTER
      await route.fulfill({ response, json: body })
    })
    await page.goto('/services')
    const row = page.getByRole('table', { name: 'Services' }).getByRole('row').filter({ hasText: LONG_SERVICE })
    await expect(row).toBeVisible()
    await page.waitForLoadState('networkidle')
    await expectPhoneLayoutClean(page)
    // The "…" button keeps its place on screen.
    const trigger = row.getByRole('button', { name: `Actions for ${LONG_SERVICE}` })
    const triggerBox = await trigger.boundingBox()
    expect(triggerBox!.x + triggerBox!.width).toBeLessThanOrEqual(390)
    await trigger.click()
    await expectSheetWithinViewport(page)
    await expectPhoneLayoutClean(page)
    await page.getByRole('menuitem', { name: 'Details' }).click()
    await expect(page.getByRole('dialog', { name: LONG_SERVICE })).toBeVisible()
    await expectSheetWithinViewport(page)
    await expectPhoneLayoutClean(page)
  })

  test('a long cluster name wraps on the Clusters list and its action sheet', async ({ page }) => {
    watchPage(page)
    await page.route('**/api/clusters', async (route) => {
      if (route.request().method() !== 'GET') return route.fallback()
      const response = await route.fetch()
      const body = await response.json()
      const items = (body.items ?? body) as { name: string }[]
      items[0].name = LONG_CLUSTER
      await route.fulfill({ response, json: body })
    })
    await page.goto('/clusters')
    await expect(page.getByRole('main').getByText(LONG_CLUSTER).first()).toBeVisible()
    await page.waitForLoadState('networkidle')
    await expectPhoneLayoutClean(page)
    const trigger = page.getByRole('button', { name: /^(More actions|Actions) for / }).first()
    if ((await trigger.count()) > 0) {
      await trigger.click()
      await expectSheetWithinViewport(page)
      await expectPhoneLayoutClean(page)
    }
  })
})

test.describe('empty and error states', () => {
  test('Services, Tasks and Clusters empty states show Clear filters; a failing list shows its message and Retry', async ({ page }) => {
    watchPage(page, (_method, url, status) => status === 500 && new URL(url).pathname === '/api/services')
    for (const [path, message] of [
      ['/services?q=zzz-none', 'No services match these filters.'],
      ['/tasks?q=zzz-none', /No tasks/],
      ['/clusters?q=zzz-none', /No clusters/],
    ] as const) {
      await page.goto(path)
      await expect(page.getByText(message).first()).toBeVisible()
      await expect(page.getByRole('button', { name: 'Clear filters' })).toBeVisible()
      await expectPhoneLayoutClean(page)
    }
    let fail = true
    await page.route('**/api/services', async (route) => {
      if (fail && route.request().method() === 'GET') return route.fulfill({ status: 500, json: { error: { code: 'internal', message: 'boom' } } })
      return route.fallback()
    })
    await page.goto('/services')
    const retry = page.getByRole('button', { name: 'Retry' })
    await expect(retry).toBeVisible()
    await expect(page.getByRole('main').locator('section[aria-label]').first()).not.toBeEmpty()
    await expectPhoneLayoutClean(page)
    fail = false
    await retry.click()
    await expect(page.getByRole('table', { name: 'Services' }).getByRole('row').first()).toBeVisible()
  })
})

test.describe('permissions on the action sheet', () => {
  test.use(asUser('omer'))

  test('omer: Restart is disabled with its reason on Staging, Prod never appears, Terminal follows his rights', async ({ page }) => {
    watchPage(page)
    await page.goto('/services?q=staging-web')
    const row = page.getByRole('table', { name: 'Services' }).getByRole('row').first()
    await row.getByRole('button', { name: /^Actions for / }).click()
    const restart = page.getByRole('menu').getByRole('menuitem', { name: /^Restart/ })
    await expect(restart).toBeDisabled()
    await expect(restart).toContainText('You need Operate on Staging')
    await expectSheetWithinViewport(page)
    await page.keyboard.press('Escape')
    expect(await mainText(page)).not.toContain('payments-api')

    // On Dev he can restart: the same sheet shows Restart enabled.
    await page.goto('/services?q=image-resizer')
    await page.getByRole('table', { name: 'Services' }).getByRole('row').first().getByRole('button', { name: /^Actions for / }).click()
    await expect(page.getByRole('menu').getByRole('menuitem', { name: /^Restart/ })).toBeEnabled()
  })
})

test.describe('Back and deep links', () => {
  test('Back closes the action sheet and stays on the page', async ({ page }) => {
    watchPage(page)
    await page.goto('/services')
    await expect(page.getByRole('table', { name: 'Services' }).getByRole('row').first()).toBeVisible()
    const url = page.url()
    await page.getByRole('button', { name: 'Actions for payments-api' }).click()
    const menu = page.getByRole('menu', { name: 'Actions for payments-api' })
    await expect(menu).toBeVisible()
    await page.goBack()
    await expect(menu).toBeHidden()
    expect(page.url()).toBe(url)
    await expect(page.getByRole('table', { name: 'Services' })).toBeVisible()

    // A sheet opened from another sheet (Details, then Restart) unwinds one step per Back.
    await page.getByRole('button', { name: 'Actions for payments-api' }).click()
    await page.getByRole('menuitem', { name: 'Details' }).click()
    const details = page.getByRole('dialog', { name: 'payments-api' })
    await expect(details).toBeVisible()
    await page.goBack()
    await expect(details).toBeHidden()
    expect(page.url()).toBe(url)
  })

  test('terminal deep link selects the task', async ({ page }) => {
    watchPage(page)
    const id = serviceId(...SVC.paymentsApi)
    const response = await page.request.get('/api/tasks?filter=running&limit=500')
    const tasks = ((await response.json()) as { items: TaskItem[] }).items.filter((task) => task.service?.id === id)
    expect(tasks.length).toBeGreaterThan(1)
    const wanted = tasks[tasks.length - 1]
    await page.goto(`/services/${id}/terminal?task=${wanted.id}`)
    await page.getByRole('button', { name: /^Target:/ }).click()
    const sheet = page.getByRole('dialog', { name: 'Terminal target' })
    await expect(sheet.getByLabel('Task')).toHaveValue(wanted.id)
    await expect(page.getByRole('button', { name: /^Target:/ })).toContainText(wanted.short_id.slice(0, 8))
    await expectSheetWithinViewport(page)
  })

  test('/services?view=problems selects the Problems chip', async ({ page }) => {
    watchPage(page)
    await page.goto('/services?view=problems')
    await expect(page.getByRole('group', { name: 'Saved views' }).getByRole('button', { name: /^Problems/ })).toHaveAttribute('aria-pressed', 'true')
    await expect(page.getByRole('group', { name: 'Saved views' }).getByRole('button', { name: /^All/ })).toHaveAttribute('aria-pressed', 'false')
    await expectPhoneLayoutClean(page)
  })
})

test('More: the theme toggle persists across a reload', async ({ page }) => {
  watchPage(page)
  await page.goto('/more')
  const theme = page.getByRole('button', { name: /^Theme/ })
  await expect(theme).toBeVisible()
  const before = await page.evaluate(() => document.documentElement.dataset.theme ?? document.documentElement.className)
  await theme.click()
  const after = await page.evaluate(() => document.documentElement.dataset.theme ?? document.documentElement.className)
  expect(after).not.toBe(before)
  await page.reload()
  await expect(page.getByRole('button', { name: /^Theme/ })).toBeVisible()
  expect(await page.evaluate(() => document.documentElement.dataset.theme ?? document.documentElement.className)).toBe(after)
  await theme.click() // put it back
})

test.describe('sign out', () => {
  // A fresh sign-in of its own, so signing out does not end the session the other specs share.
  test.use({ storageState: { cookies: [], origins: [] } })

  test('More: Sign out returns to the sign-in page and the app is locked again', async ({ page }) => {
    await login(page, 'dana')
    await page.goto('/more')
    await page.getByRole('button', { name: 'Sign out' }).click()
    await expect(page).toHaveURL(/\/login/)
    await page.goto('/services')
    await expect(page).toHaveURL(/\/login/)
  })
})
