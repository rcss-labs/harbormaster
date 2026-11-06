import { expect, test, type Page } from './fixtures/test'
import { SVC, serviceUrl } from './fixtures/ids'
import { login, reviewAndApply, serviceId } from './helpers'

// Runs in chromium-degraded: DescribeScheduledActions always fails, so the Deploy tab gets a partial response and every
// other part must keep working. Mutations only touch the degraded server's own schema.
test.describe.configure({ mode: 'serial' })

test.beforeEach(async ({ page }) => {
  await login(page, 'pavel')
})

const service = SVC.paymentsApi
const serviceName = service[3]
const deployUrl = serviceUrl(...service, 'deploy')

async function scheduledActionsGroup(page: Page) {
  const group = page.getByRole('heading', { name: 'Scheduled actions', exact: true }).locator('xpath=..')
  await expect(group).toBeVisible()
  return group
}

test('the Deploy tab renders every panel and only scheduled actions are degraded', async ({ page }) => {
  await page.goto(deployUrl)
  const main = page.getByRole('main')
  for (const heading of ['Containers', 'Task size', 'Service', 'Auto scaling']) {
    await expect(main.getByRole('heading', { name: heading, exact: true }).first()).toBeVisible()
  }
  await expect(main.getByRole('table', { name: 'Scaling policies' })).toBeVisible()
  await expect(page.getByRole('combobox', { name: 'Image tag' })).toBeEnabled()

  const group = await scheduledActionsGroup(page)
  await expect(group.getByRole('alert')).toContainText('Scheduled actions could not be read')
  await expect(group.getByRole('button', { name: 'Retry' })).toBeVisible()
  await expect(group.getByRole('button', { name: 'Add scheduled action' })).toBeDisabled()
  // Nothing else on the tab reports an error.
  await expect(main.getByRole('alert')).toHaveCount(1)
})

test('an environment change applies although scheduled actions are unreadable', async ({ page }) => {
  await page.goto(deployUrl)
  await page.getByRole('button', { name: 'Add variable' }).click()
  const names = page.getByRole('textbox', { name: /^Variable name \d+$/ })
  const row = await names.count()
  await names.nth(row - 1).fill('PARTIAL_DEPLOY_CHECK')
  await page.getByRole('textbox', { name: `Variable value ${row}` }).fill('on')
  await reviewAndApply(page, serviceName, /^Register and deploy :\d+$/, 'Env edit while scheduled actions are unreadable')
  // An env edit registers a revision and starts a deployment, so the toast names the revision; "Applied the changes to …"
  // is what the same toast says when the response carries no target revision.
  await expect(page.getByText(new RegExp(`^(Deploying ${serviceName} to :\\d+|Applied the changes to ${serviceName})\\.$`))).toBeVisible()
})

test('adding a scheduled action is refused with the unreadable-part message', async ({ page }) => {
  // The editor is disabled in the UI, so the refusal is checked on the API behind it.
  const id = serviceId(...service)
  const settingsResponse = await page.request.get(`/api/services/${id}/deploy-settings`)
  expect(settingsResponse.ok()).toBe(true)
  const settings = (await settingsResponse.json()) as { version: string; deployed_revision: number }
  const me = (await (await page.request.get('/api/me')).json()) as { csrf_token: string }
  const response = await page.request.post(`/api/services/${id}/changeset`, {
    headers: { 'X-CSRF-Token': me.csrf_token },
    data: {
      base_revision: settings.deployed_revision,
      base_version: settings.version,
      reason: 'Add a schedule while they cannot be read',
      scaling: { put_scheduled_actions: [{ name: 'office-hours', schedule: 'cron(0 7 * * ? *)', timezone: 'UTC', min_capacity: 2, max_capacity: null }] },
    },
  })
  expect(response.status()).toBe(422)
  expect(await response.text()).toContain('Scheduled actions could not be read; retry before changing them.')
})
