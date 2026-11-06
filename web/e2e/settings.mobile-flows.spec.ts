import { expectPhoneLayoutClean, watchPage } from './fixtures/phone'
import { asUser, expect, test } from './fixtures/test'

// Phone flows that save. They run after the flow specs, so they do not assume seed state and put back what they change.
test.use({ ...asUser('sam'), viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true, deviceScaleFactor: 3 })

test('General: the save bar saves the stopped-tasks window and shows Saved.', async ({ page }) => {
  watchPage(page)
  await page.goto('/settings/general')
  const input = page.getByLabel('Show stopped tasks from the last X minutes')
  await expect(input).toBeEnabled()
  const original = await input.inputValue()
  try {
    await input.fill('0')
    await page.getByRole('button', { name: 'Save' }).click()
    await expect(page.getByText('Enter a number from 1 to 1440.')).toBeVisible()
    await input.fill('45')
    await page.getByRole('button', { name: 'Save' }).click()
    await expect(page.getByText('Saved.')).toBeVisible()
    await expectPhoneLayoutClean(page)
    await expect.poll(async () => (await (await page.request.get('/api/settings')).json()).stopped_tasks_window_minutes).toBe(45)
  } finally {
    await page.request.put('/api/settings', { data: { stopped_tasks_window_minutes: Number(original) || 10 } })
  }
})

test('Users: a preset chosen in the permissions sheet is saved and the sheet shows it', async ({ page }) => {
  watchPage(page)
  await page.goto('/settings/users')
  await page.getByRole('button', { name: 'Actions for oncall' }).click()
  await page.getByRole('menuitem', { name: 'Edit permissions' }).click()
  const perms = page.getByRole('dialog', { name: 'Edit permissions for oncall' })
  const dev = perms.getByRole('button', { name: /^oncall on Dev:/ })
  const before = await dev.getAttribute('aria-label')
  const original = before?.split(': ')[1] ?? 'None'
  try {
    await dev.click()
    await page.getByLabel('Preset for oncall on Dev').selectOption('Viewer')
    await expect(perms.getByRole('button', { name: 'oncall on Dev: Viewer' })).toBeVisible()
    const data = await (await page.request.get('/api/permissions')).json()
    const oncall = data.users.find((user: { username: string }) => user.username === 'oncall')
    const devId = data.tags.find((tag: { name: string }) => tag.name === 'Dev').id
    expect(oncall.perms[String(devId)]).toBe(1)
    await expectPhoneLayoutClean(page)
  } finally {
    const data = await (await page.request.get('/api/permissions')).json()
    const oncall = data.users.find((user: { username: string }) => user.username === 'oncall')
    const devId = data.tags.find((tag: { name: string }) => tag.name === 'Dev').id
    const presets: Record<string, number> = { None: 0, Viewer: 1, Operator: 7, Deployer: 31, Full: 63 }
    if (original in presets) await page.request.put(`/api/permissions/${oncall.id}/${devId}`, { data: { actions: presets[original] } })
  }
})
