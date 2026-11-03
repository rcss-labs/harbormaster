import { asUser, expect, test } from './fixtures/test'
import { clusterUrl } from './fixtures/ids'

// Read-only: runs in the observe project.
const CORE_SERVICES = clusterUrl('482109375512', 'us-east-1', 'core-services')

test.describe('cluster as oncall', () => {
  test.use(asUser('oncall'))

  test('core-services lists its four services in the services table', async ({ page }) => {
    await page.goto(CORE_SERVICES)
    await expect(page.getByRole('heading', { level: 1, name: 'core-services' })).toBeVisible()
    const services = page.getByRole('table', { name: 'Services' })
    // Header row plus four services.
    await expect(services.getByRole('row')).toHaveCount(5)

    const notifications = services.getByRole('row', { name: /notifications/ })
    // The seeded rollout (Deploying, with a violet rollout slot) finishes within about a minute of startup, before
    // the observe project runs; smoke.spec asserts the Deploying status while it lasts. Here: what holds for the whole run.
    await expect(notifications.getByText(/^(Deploying|Steady)$/)).toBeVisible()
    await expect(notifications).toContainText(/\d+ \/ 2/)
  })
})

test.describe('pinned clusters as oncall', () => {
  test.use(asUser('oncall'))

  test('pins a cluster from its page, shows account name and id in the sidebar, and unpins it', async ({ page }) => {
    await page.goto(CORE_SERVICES)
    await page.getByRole('button', { name: 'Pin core-services' }).click()
    const pinned = page.getByRole('navigation', { name: 'Pinned' })
    const link = pinned.getByRole('link', { name: /core-services/ })
    await expect(link).toContainText('homelab-admin · 482109375512')
    await expect(link).toHaveAttribute('href', /\/clusters\//)

    await page.getByRole('button', { name: 'Unpin core-services' }).click()
    await expect(pinned.getByRole('link')).toHaveCount(0)
    await expect(pinned).toContainText('Pin a service or cluster from its page.')
  })
})

test.describe('cluster as omer', () => {
  test.use(asUser('omer'))

  test('a cluster he cannot see answers 404 and shows no data', async ({ page }) => {
    const apiResponse = page.waitForResponse((response) => new URL(response.url()).pathname.startsWith('/api/clusters/'))
    await page.goto(CORE_SERVICES)
    // Never 403: existence of a hidden cluster is not revealed.
    expect((await apiResponse).status()).toBe(404)
    await expect(page.getByRole('heading', { name: 'No access' })).toBeVisible()
    await expect(page.getByRole('table', { name: 'Services' })).toHaveCount(0)
    expect(await page.locator('body').textContent()).not.toContain('payments-api')
  })
})
