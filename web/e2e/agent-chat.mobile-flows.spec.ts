import { asUser, expect, test } from './fixtures/test'
import { saveScreenshot } from './helpers'

// Writes a conversation, so it runs with the phone specs that change data.
test.use({ ...asUser('sam'), viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true, deviceScaleFactor: 3 })

test('the agent opens as a bottom sheet on a phone and answers', async ({ page }) => {
  await page.goto('/clusters')
  await page.getByRole('button', { name: 'Ask AI' }).click()
  const sheet = page.getByRole('dialog', { name: 'Agent' })
  await expect(sheet).toBeVisible()
  const box = (await sheet.boundingBox())!
  const viewport = page.viewportSize()!
  expect(box.x).toBeGreaterThanOrEqual(0)
  expect(box.x + box.width).toBeLessThanOrEqual(viewport.width + 1)
  expect(box.y + box.height).toBeGreaterThanOrEqual(viewport.height - 1) // anchored to the bottom edge
  await sheet.getByRole('textbox', { name: 'Message' }).fill('How is the estate doing?')
  await sheet.getByRole('button', { name: 'Send' }).click()
  await expect(sheet.getByText('This is a canned answer from the demo model.')).toBeVisible()
  await saveScreenshot(page, 'agent-sheet-phone.png')
})
