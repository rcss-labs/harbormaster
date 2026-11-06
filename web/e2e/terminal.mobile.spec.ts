import type { Page } from '@playwright/test'
import { asUser, expect, test } from './fixtures/test'
import { SVC, serviceUrl } from './fixtures/ids'
import { expectSheetWithinViewport, phoneViolations, watchPage } from './fixtures/phone'
import { measureBox, measureBoxes } from './helpers'

// Read-only phone checks of the terminal. Sessions run against the fake backend's ECS Exec path and change no estate data.
test.use({ ...asUser('sam'), viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true, deviceScaleFactor: 3 })

let problems: string[] = []
test.beforeEach(({ page }) => {
  problems = watchPage(page)
})

async function expectOwnLayoutClean(page: Page) {
  expect(problems, 'page errors, console errors and failing responses').toEqual([])
  expect(await phoneViolations(page), 'phone layout violations').toEqual([])
}

const paymentsTerminal = serviceUrl(...SVC.paymentsApi, 'terminal')
const execOffTerminal = serviceUrl('482109375512', 'eu-central-1', 'edge-proxy', 'cert-renewer', 'terminal')

/** Frames the page sends on the exec WebSocket, as text (binary frames are keystrokes). */
function recordExecFrames(page: Page): string[] {
  const frames: string[] = []
  page.on('websocket', (socket) => {
    if (!socket.url().includes('/exec')) return
    socket.on('framesent', (frame) => frames.push(typeof frame.payload === 'string' ? frame.payload : frame.payload.toString('latin1')))
  })
  return frames
}

async function connectThroughSheet(page: Page) {
  await page.getByRole('button', { name: /^Target:/ }).click()
  const sheet = page.getByRole('dialog', { name: 'Terminal target' })
  await expect(sheet.getByLabel('Container')).toHaveValue(/.+/)
  await expectSheetWithinViewport(page)
  await sheet.getByRole('button', { name: 'Connect' }).click()
  await expect(page.locator('.xterm-rows')).toContainText('Starting session with SessionId: ecs-execute-command-', { timeout: 15_000 })
}

test('connects through the target sheet, keys row sends ^C, hostname echoes, disconnect', async ({ page }) => {
  const frames = recordExecFrames(page)
  await page.goto(paymentsTerminal)
  await expect(page.getByTestId('terminal-host')).toBeVisible()
  await expect(page.getByRole('toolbar', { name: 'Terminal keys' })).toHaveCount(0)
  await expectOwnLayoutClean(page)

  await connectThroughSheet(page)
  const keys = page.getByRole('toolbar', { name: 'Terminal keys' })
  await expect(keys).toBeVisible()
  await expect(page.getByRole('navigation', { name: 'Main' })).toBeHidden()
  await expectOwnLayoutClean(page)

  await page.locator('.xterm-helper-textarea').focus()
  await page.keyboard.type('hostname')
  await page.keyboard.press('Enter')
  await expect(page.locator('.xterm-rows')).toContainText('ip-10-20-1-149.ec2.internal')

  const before = frames.length
  await keys.getByRole('button', { name: 'Send Control-C' }).tap()
  await expect.poll(() => frames.slice(before).includes('\x03')).toBe(true)

  await page.getByRole('button', { name: 'Disconnect' }).click()
  await expect(page.getByRole('toolbar', { name: 'Terminal keys' })).toHaveCount(0)
  await expect(page.getByRole('navigation', { name: 'Main' })).toBeVisible()
})

test('every key sends its bytes', async ({ page }) => {
  const frames = recordExecFrames(page)
  await page.goto(paymentsTerminal)
  await connectThroughSheet(page)
  const keys = page.getByRole('toolbar', { name: 'Terminal keys' })
  const expected: [string, string][] = [
    ['Send Escape', '\x1b'],
    ['Send Tab', '\t'],
    ['Send Up', '\x1b[A'],
    ['Send Down', '\x1b[B'],
    ['Send Left', '\x1b[D'],
    ['Send Right', '\x1b[C'],
  ]
  for (const [name, bytes] of expected) {
    const before = frames.length
    await keys.getByRole('button', { name }).tap()
    await expect.poll(() => frames.slice(before).includes(bytes), { message: name }).toBe(true)
  }
})

test.describe('with the keyboard up (a 500 px tall viewport stands in for it)', () => {
  test.use({ viewport: { width: 390, height: 500 } })

  test('host bottom sits above the keys row at 390×500', async ({ page }) => {
      await page.goto(paymentsTerminal)
    await connectThroughSheet(page)
    const keys = page.getByRole('toolbar', { name: 'Terminal keys' })
    const host = page.getByTestId('terminal-host')
    const [hostBox, keysBox] = await measureBoxes(host, keys)
    expect(hostBox.y + hostBox.height, `host ${JSON.stringify(hostBox)} vs keys ${JSON.stringify(keysBox)}`).toBeLessThanOrEqual(keysBox.y + 1)
    expect(keysBox.y + keysBox.height).toBeLessThanOrEqual(500 + 1)
    expect(await page.evaluate(() => window.visualViewport?.height)).toBe(500)
    await expectOwnLayoutClean(page)
  })

  test('xterm refits its rows when the viewport height changes', async ({ page }) => {
    await page.goto(paymentsTerminal)
    await connectThroughSheet(page)
    const rowsAt = () => page.locator('.xterm-rows > div').count()
    const tall = await measureBox(page.getByTestId('terminal-host'))
    await page.setViewportSize({ width: 390, height: 700 })
    await expect.poll(async () => (await measureBox(page.getByTestId('terminal-host'))).height).toBeGreaterThan(tall.height + 100)
    await expect.poll(rowsAt).toBeGreaterThan(0)
    const bigRows = await page.locator('.xterm-screen').evaluate((el) => el.getBoundingClientRect().height)
    await page.setViewportSize({ width: 390, height: 500 })
    await expect.poll(() => page.locator('.xterm-screen').evaluate((el) => el.getBoundingClientRect().height)).toBeLessThan(bigRows)
    const [hostBox, keysBox] = await measureBoxes(page.getByTestId('terminal-host'), page.getByRole('toolbar', { name: 'Terminal keys' }))
    expect(hostBox.y + hostBox.height).toBeLessThanOrEqual(keysBox.y + 1)
  })
})

test('the Session info button opens a sheet and the host does not overlap the toolbar', async ({ page }) => {
  await page.goto(paymentsTerminal)
  const host = page.getByTestId('terminal-host')
  const toolbarButton = page.getByRole('button', { name: 'Session details' })
  const [h, t] = await measureBoxes(host, toolbarButton)
  expect(h.y).toBeGreaterThanOrEqual(t.y + t.height - 1)
  await toolbarButton.click()
  const sheet = page.getByRole('dialog', { name: 'Session' })
  await expect(sheet).toContainText('Idle timeout')
  await expect(sheet).toContainText('Keystrokes and output are not recorded.')
  await expectSheetWithinViewport(page)
  await expectOwnLayoutClean(page)
})

test('exec-off service shows the banner at 390', async ({ page }) => {
  await page.goto(execOffTerminal)
  await expect(page.getByText('enable_execute_command = true')).toBeVisible()
  await expect(page.getByRole('button', { name: 'Connect' })).toHaveCount(0)
  await expectOwnLayoutClean(page)
})

test('task-terminal route renders the panel for a standalone task', async ({ page }) => {
  const response = await page.request.get('/api/tasks?filter=running')
  const items = (await response.json()).items as { short_id: string; service: unknown; exec_enabled: boolean }[]
  const task = items.find((item) => !item.service && item.exec_enabled)
  test.skip(!task, 'the fake estate has no standalone task with ECS Exec on')
  await page.goto(`/tasks/${task!.short_id}/terminal`)
  await expect(page.getByRole('button', { name: /^Target:/ })).toBeVisible()
  await expect(page.getByTestId('terminal-host')).toBeVisible()
  await expectOwnLayoutClean(page)
})
