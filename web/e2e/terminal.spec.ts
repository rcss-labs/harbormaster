import { expect, test } from './fixtures/test'
import { login, serviceId } from './helpers'

// Ids come from the fake estate (internal/awsx/fake/seed.go): all clusters live in the homelab account.
const HOMELAB_ACCOUNT_ID = '482109375512'
const paymentsApi = serviceId(HOMELAB_ACCOUNT_ID, 'us-east-1', 'core-services', 'payments-api')
const certRenewer = serviceId(HOMELAB_ACCOUNT_ID, 'eu-central-1', 'edge-proxy', 'cert-renewer')

test('opens a terminal from the Tasks tab and runs hostname', async ({ page }) => {
  await login(page, 'sam')
  await page.goto(`/services/${paymentsApi}/tasks`)
  // The tab bar also has a Terminal link, so scope to the task table.
  await page.getByRole('table').getByRole('link', { name: 'Terminal', exact: true }).first().click()
  await expect(page).toHaveURL(/\/services\/.+\/terminal\?task=/)

  await page.getByRole('button', { name: 'Connect' }).click()
  await expect(page.locator('.xterm-rows')).toContainText('Starting session with SessionId: ecs-execute-command-', { timeout: 15_000 })

  await page.locator('.xterm-helper-textarea').focus()
  await page.keyboard.type('hostname')
  await page.keyboard.press('Enter')
  await expect(page.locator('.xterm-rows')).toContainText('ip-10-20-1-149.ec2.internal')

  await page.keyboard.type('exit')
  await page.keyboard.press('Enter')
  await expect(page.locator('.xterm-rows')).toContainText('Exiting session with sessionId')
})

test('exec-off service shows the Terraform banner', async ({ page }) => {
  await login(page, 'sam')
  await page.goto(`/services/${certRenewer}/terminal`)
  await expect(page.getByText('enable_execute_command = true')).toBeVisible()
  await expect(page.getByRole('button', { name: 'Connect' })).toHaveCount(0)
})

test('at 390px the terminal and the Session panel do not overlap', async ({ page }) => {
  await login(page, 'sam')
  await page.setViewportSize({ width: 390, height: 800 })
  await page.goto(`/services/${paymentsApi}/terminal`)
  const host = page.getByTestId('terminal-host')
  await expect(host).toBeVisible()
  const sessionTitle = page.getByText('Session', { exact: true }).first()
  await expect(sessionTitle).toBeVisible()
  const h = (await host.boundingBox())!
  const s = (await sessionTitle.boundingBox())!
  const intersects = h.x < s.x + s.width && s.x < h.x + h.width && h.y < s.y + s.height && s.y < h.y + h.height
  expect(intersects, `host ${JSON.stringify(h)} vs session ${JSON.stringify(s)}`).toBe(false)
})
