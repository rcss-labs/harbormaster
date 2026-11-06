import type { Page } from '@playwright/test'
import { asUser, expect, test } from './fixtures/test'
import { SVC, serviceUrl } from './fixtures/ids'
import { simulateKeyboard, watchPage } from './fixtures/phone'
import { measureBox, measureBoxes } from './helpers'

// The iOS keyboard path. Chromium resizes the layout viewport when a keyboard opens (interactive-widget=resizes-content), so
// a smaller viewport never exercises --hm-vvh / --hm-vvt. Here the layout stays 390 x 844 and the variables that
// useVisualViewport writes on iOS are set by hand: a 420 px visible height, as with the keyboard up. Nothing may sit below it.
test.use({ ...asUser('sam'), viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true, deviceScaleFactor: 3 })

const VISIBLE = 420

test.beforeEach(({ page }) => {
  watchPage(page)
})

async function connectThroughSheet(page: Page) {
  await page.getByRole('button', { name: /^Target:/ }).click()
  const sheet = page.getByRole('dialog', { name: 'Terminal target' })
  await expect(sheet.getByLabel('Container')).toHaveValue(/.+/)
  await sheet.getByRole('button', { name: 'Connect' }).click()
  await expect(page.locator('.xterm-rows')).toContainText('Starting session with SessionId: ecs-execute-command-', { timeout: 15_000 })
}

test('terminal: the host ends above the keys row and the keys row sits on the visible bottom', async ({ page }) => {
  await page.goto(serviceUrl(...SVC.paymentsApi, 'terminal'))
  await connectThroughSheet(page)
  await simulateKeyboard(page, VISIBLE)
  const keys = page.getByRole('toolbar', { name: 'Terminal keys' })
  const host = page.getByTestId('terminal-host')
  await expect
    .poll(async () => {
      const keysBox = await measureBox(keys)
      return Math.round(keysBox.y + keysBox.height)
    })
    .toBeLessThanOrEqual(VISIBLE + 1)
  const [hostBox, keysBox] = await measureBoxes(host, keys)
  expect(hostBox.y + hostBox.height, `host ${JSON.stringify(hostBox)} vs keys ${JSON.stringify(keysBox)}`).toBeLessThanOrEqual(keysBox.y + 0.5)
  expect(hostBox.height).toBeGreaterThan(60)
})

test('logs: the newest lines are above the visible bottom with the keyboard up', async ({ page }) => {
  await page.goto(serviceUrl(...SVC.paymentsApi, 'logs'))
  const box = page.getByRole('log')
  await expect(box.getByRole('row').first()).toBeVisible()
  await simulateKeyboard(page, VISIBLE)
  await page.getByRole('main').evaluate((main) => (main.scrollTop = main.scrollHeight))
  await box.evaluate((el) => (el.scrollTop = el.scrollHeight))
  await expect
    .poll(async () => {
      const last = await measureBox(box.getByRole('row').last())
      return Math.round(last.y + last.height)
    })
    .toBeLessThanOrEqual(VISIBLE)
  const logBox = await measureBox(box)
  expect(logBox.y + logBox.height, 'the log box ends above the keyboard').toBeLessThanOrEqual(VISIBLE)
})

test('deploy: the sticky bar sits on the visible bottom, not under the keyboard', async ({ page }) => {
  await page.goto(serviceUrl(...SVC.paymentsApi, 'deploy'))
  await page.getByRole('region', { name: 'Service' }).getByRole('spinbutton', { name: 'Desired count' }).fill('4')
  const bar = page.getByRole('region', { name: 'Pending changes' })
  await expect(bar).toBeVisible()
  await simulateKeyboard(page, VISIBLE)
  await expect
    .poll(async () => {
      const box = await measureBox(bar)
      return Math.round(box.y + box.height)
    })
    .toBeLessThanOrEqual(VISIBLE + 1)
  const box = await measureBox(bar)
  expect(box.y, 'the bar is on screen').toBeGreaterThanOrEqual(0)
})
