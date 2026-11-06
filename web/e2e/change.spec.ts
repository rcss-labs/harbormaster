import { expect, test, type Page } from './fixtures/test'
import { login, pickImageTag, reviewAndApply, serviceId } from './helpers'

// Ids come from the fake estate (internal/awsx/fake/seed.go): both clusters live in the homelab account, us-east-1.
const HOMELAB_ACCOUNT_ID = '482109375512'
const REGION = 'us-east-1'
const paymentsApi = serviceId(HOMELAB_ACCOUNT_ID, REGION, 'core-services', 'payments-api')
const imageResizer = serviceId(HOMELAB_ACCOUNT_ID, REGION, 'background-jobs', 'image-resizer')

async function openOperationsLog(page: Page, id: string) {
  await page.goto(`/services/${id}/operations`)
  await expect(page.getByRole('heading', { level: 1 })).toBeVisible()
}

/** Reviews the Deploy tab's pending edits and applies them as one change set (register plus deploy). */
async function registerAndDeploy(page: Page, serviceName: string, revision: number) {
  await reviewAndApply(page, serviceName, `Register and deploy :${revision}`)
  await expect(page.getByText(`Deploying ${serviceName} to :${revision}.`)).toBeVisible()
}

// The flows share one seeded estate and run in this order: register/deploy moves payments-api to :43 (the
// stop-a-task flow then runs on its steady state), and the operator check must precede the OOM rollback,
// whose open rollout disables Restart.
test.describe.configure({ mode: 'serial' })

test('register and deploy a new image tag', async ({ page }) => {
  await login(page, 'pavel')
  await page.goto(`/services/${paymentsApi}/deploy`)

  await pickImageTag(page, '1.18.4')
  await registerAndDeploy(page, 'payments-api', 43)

  // The deploy is accepted at once but the service view only changes on the next 5 s poll, so allow
  // for it (plus slack) before the card shows. The 15 s toast wait below starts once the card is up.
  await expect(page.getByText('Rollout in progress')).toBeVisible({ timeout: 10_000 })
  await expect(page.getByText(':42 → :43')).toBeVisible({ timeout: 10_000 })
  await expect(page.getByText('payments-api is running :43 on all 3 tasks.')).toBeVisible({ timeout: 15_000 })

  await page.getByRole('main').getByRole('link', { name: /^Task definition/ }).click()
  await expect(page.getByRole('row', { name: /:43\s+Deployed/ })).toBeVisible()

  await openOperationsLog(page, paymentsApi)
  await expect(page.getByText('Registered task definition payments-api:43 (image 1.18.4, 512 CPU / 1024 MiB).')).toBeVisible()
  await expect(page.getByText('Deployed payments-api:43.')).toBeVisible()
  // The outcome logger runs every 5 s, so allow a tick after the toast.
  await expect(async () => {
    await page.reload()
    await expect(page.getByText('Deployment of payments-api:43 completed.')).toBeVisible({ timeout: 2_000 })
  }).toPass({ timeout: 15_000 })
})

// Runs before the OOM flow: image-resizer's rollback keeps a rollout open, which disables Restart.
test('operator cannot register', async ({ page }) => {
  await login(page, 'omer')
  await page.goto(`/services/${imageResizer}/task-definition`)

  await expect(page.getByText('Read only.')).toBeVisible()
  await expect(page.getByText('You need Edit task definition on Dev to register a new revision.')).toBeVisible()
  await expect(page.getByRole('button', { name: 'Restart (new deployment)' })).toBeEnabled()
})

test('OOM revision fails and rolls back', async ({ page }) => {
  await login(page, 'pavel')
  await page.goto(`/services/${imageResizer}/deploy`)

  // Same 512 MiB memory: the fake estate OOM-kills image-resizer's new revision.
  await page.getByRole('button', { name: 'Add variable' }).click()
  await page.getByRole('textbox', { name: 'Variable name 2' }).fill('X')
  await page.getByRole('textbox', { name: 'Variable value 2' }).fill('1')
  await registerAndDeploy(page, 'image-resizer', 7)

  await expect(page.getByText('image-resizer: revision :7 failed to start. Rolled back to :6.')).toBeVisible({ timeout: 20_000 })

  await openOperationsLog(page, imageResizer)
  await expect(async () => {
    await page.reload()
    await expect(page.getByText('Deployment of image-resizer:7 failed: ECS deployment circuit breaker: tasks failed to start.')).toBeVisible({
      timeout: 2_000,
    })
    // The poller records this line asynchronously, and it can take longer on a loaded machine.
  }).toPass({ timeout: 30_000 })
})

test('stop a task', async ({ page }) => {
  await login(page, 'pavel')
  await page.goto(`/services/${paymentsApi}/tasks`)

  const firstStoppable = page.getByRole('row').filter({ has: page.getByRole('button', { name: 'Stop', exact: true }) }).first()
  const taskId = ((await firstStoppable.getByRole('cell').first().textContent()) ?? '').trim()
  expect(taskId).toMatch(/^[0-9a-f]{12}$/)
  await firstStoppable.getByRole('button', { name: 'Stop', exact: true }).click()
  const dialog = page.getByRole('dialog')
  await expect(dialog.getByRole('heading', { name: `Stop task ${taskId}?`, exact: true })).toBeVisible()
  await dialog.getByRole('button', { name: 'Stop task' }).click()

  const row = page.getByRole('row').filter({ hasText: taskId })
  await expect(row.getByText('Stopped', { exact: true })).toBeVisible({ timeout: 10_000 })

  await openOperationsLog(page, paymentsApi)
  await expect(page.getByText(`Stopped task ${taskId}.`).first()).toBeVisible()
})
