import { asUser, expect, test } from './fixtures/test'

// Read-only: runs in the phone project against the unmutated seed. A phone never scales, whatever is stored.
test.use(asUser('sam'))

test('no data-scale at 390 even with harbormaster-scale=200 stored', async ({ page }) => {
  await page.addInitScript(() => localStorage.setItem('harbormaster-scale', '200'))
  await page.goto('/')
  await expect(page.getByRole('main')).toBeVisible()
  expect(await page.evaluate(() => document.documentElement.dataset.scale ?? null)).toBeNull()
  expect(await page.evaluate(() => getComputedStyle(document.documentElement).zoom)).toBe('1')
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
  // The UI scale button is desktop-only.
  await expect(page.getByRole('button', { name: 'UI scale' })).toHaveCount(0)
})
