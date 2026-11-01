import { asUser, expect, test, type Page } from './fixtures/test'
import { login, saveScreenshot } from './helpers'

// The storage tier's first pass runs a few seconds after the server starts.
const POLL_TIMEOUT = 30_000

// One EFS card per file system: a section named after it, holding an h3.
const fsCards = (page: Page) => page.getByRole('region').filter({ has: page.getByRole('heading', { level: 3 }) })

test('admin sees five file systems including Not mounted', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 900 })
  await login(page, 'pavel')
  await page.goto('/storage')

  const archive = page.getByRole('region', { name: 'uploads-archive' })
  await expect(archive).toBeVisible({ timeout: POLL_TIMEOUT })
  await expect(archive.getByText('Not mounted')).toBeVisible()
  await expect(fsCards(page)).toHaveCount(5)
  await expect(page.getByText('337.0 GiB in total')).toBeVisible()

  await saveScreenshot(page, 'storage.png')
})

test('dev user sees four', async ({ page }) => {
  await login(page, 'omer')
  await page.goto('/storage')
  await expect(fsCards(page).first()).toBeVisible({ timeout: POLL_TIMEOUT })
  await expect(fsCards(page)).toHaveCount(4)
  await expect(page.getByRole('region', { name: 'uploads-archive' })).toHaveCount(0)
})

// plan 06 (task 19): the contents of a card and which file systems each role can name. The tests above already cover
// the five cards and the Not mounted flag for pavel, and omer's count of four.
test.describe('storage as pavel', () => {
  test.use(asUser('pavel'))

  test('jenkins-home is mounted by jenkins-controller at /var/jenkins_home', async ({ page }) => {
    await page.goto('/storage')
    const card = page.getByRole('region', { name: 'jenkins-home' })
    await expect(card).toBeVisible({ timeout: POLL_TIMEOUT })
    await expect(card.getByRole('link', { name: 'jenkins-controller' })).toBeVisible()
    await expect(card.getByText('/var/jenkins_home')).toBeVisible()
  })
})

test.describe('storage as omer', () => {
  test.use(asUser('omer'))

  test('sees the file systems Dev services mount and not the unmounted one', async ({ page }) => {
    await page.goto('/storage')
    await expect(page.getByRole('region', { name: 'reports-data' })).toBeVisible({ timeout: POLL_TIMEOUT })
    for (const name of ['jenkins-home', 'search-index', 'shared-certs']) {
      await expect(page.getByRole('region', { name })).toBeVisible()
    }
    await expect(page.getByRole('region', { name: 'uploads-archive' })).toHaveCount(0)
  })
})
