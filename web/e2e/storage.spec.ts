import { asUser, expect, test, type Page } from './fixtures/test'
import { login, saveScreenshot } from './helpers'

// The storage tier's first pass runs a few seconds after the server starts.
const POLL_TIMEOUT = 30_000

// One table row per file system.
const fsTable = (page: Page) => page.getByRole('table', { name: 'File systems' })
const fsRows = (page: Page) => fsTable(page).getByRole('row').filter({ has: page.getByRole('cell') })
const fsRow = (page: Page, name: string) => fsRows(page).filter({ hasText: name })

test('admin sees five file systems including Not mounted', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 900 })
  await login(page, 'sam')
  await page.goto('/storage')

  const archive = fsRow(page, 'uploads-archive')
  await expect(archive).toBeVisible({ timeout: POLL_TIMEOUT })
  await expect(archive.getByText('Not mounted')).toBeVisible()
  await expect(fsRows(page)).toHaveCount(5)
  await expect(page.getByRole('list', { name: 'Storage summary' })).toContainText('337.0 GiB')

  await saveScreenshot(page, 'storage.png')
})

test('dev user sees four', async ({ page }) => {
  await login(page, 'omer')
  await page.goto('/storage')
  await expect(fsRows(page).first()).toBeVisible({ timeout: POLL_TIMEOUT })
  await expect(fsRows(page)).toHaveCount(4)
  await expect(fsRow(page, 'uploads-archive')).toHaveCount(0)
})

// plan 06 (task 19): the contents of a row and which file systems each role can name. The tests above already cover
// the five rows and the Not mounted flag for sam, and omer's count of four.
test.describe('storage as sam', () => {
  test.use(asUser('sam'))

  test('jenkins-home is mounted by jenkins-controller at /var/jenkins_home', async ({ page }) => {
    await page.goto('/storage')
    const card = fsRow(page, 'jenkins-home')
    await expect(card).toBeVisible({ timeout: POLL_TIMEOUT })
    await expect(card.getByRole('link', { name: 'jenkins-controller' })).toBeVisible()
    await expect(card.getByText('/var/jenkins_home')).toBeVisible()
  })
})

test.describe('storage as omer', () => {
  test.use(asUser('omer'))

  test('sees the file systems Dev services mount and not the unmounted one', async ({ page }) => {
    await page.goto('/storage')
    await expect(fsRow(page, 'reports-data')).toBeVisible({ timeout: POLL_TIMEOUT })
    for (const name of ['jenkins-home', 'search-index', 'shared-certs']) {
      await expect(fsRow(page, name)).toBeVisible()
    }
    await expect(fsRow(page, 'uploads-archive')).toHaveCount(0)
  })
})
