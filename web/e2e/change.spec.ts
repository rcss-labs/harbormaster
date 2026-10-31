import { expect, test, type Page } from '@playwright/test'
import { login, serviceId } from './helpers'

// Ids come from the fake estate (internal/awsx/fake/seed.go): both clusters live in the homelab account, us-east-1.
const HOMELAB_ACCOUNT_ID = '482109375512'
const REGION = 'us-east-1'
const paymentsApi = serviceId(HOMELAB_ACCOUNT_ID, REGION, 'core-services', 'payments-api')
const imageResizer = serviceId(HOMELAB_ACCOUNT_ID, REGION, 'background-jobs', 'image-resizer')

async function openOperationsLog(page: Page, id: string) {
  await page.goto(`/services/${id}/operations`)
  await expect(page.getByRole('heading', { level: 1 })).toBeVisible()
}

/** Edits, reviews and registers the draft, then accepts the "Deploy it now?" prompt. */
async function registerAndDeploy(page: Page, revision: number) {
  await page.getByRole('button', { name: 'Review and save' }).click()
  const review = page.getByRole('dialog')
  await expect(review.getByText(new RegExp(`^Register .+:${revision}\\?$`))).toBeVisible()
  await review.getByRole('button', { name: `Register revision :${revision}` }).click()
  const prompt = page.getByRole('dialog')
  await expect(prompt.getByText(`Revision :${revision} is registered. Deploy it now?`)).toBeVisible()
  await prompt.getByRole('button', { name: `Deploy :${revision}` }).click()
}

test('register and deploy a new image tag', async ({ page }) => {
  await login(page, 'pavel')
  await page.goto(`/services/${paymentsApi}/task-definition`)

  await page.getByLabel('Image tag').selectOption({ label: '1.18.4 · newest' })
  await page.getByRole('button', { name: 'Review and save' }).click()
  await expect(page.getByRole('dialog').getByText('Register payments-api:43?')).toBeVisible()
  await page.getByRole('button', { name: 'Register revision :43' }).click()
  const prompt = page.getByRole('dialog')
  await expect(prompt.getByText('Revision :43 is registered. Deploy it now?')).toBeVisible()
  // The pending-revision banner behind the dialog has its own "Deploy :43" button, so scope to the dialog.
  await prompt.getByRole('button', { name: 'Deploy :43' }).click()

  // The service view refreshes from the poller (about 5 s), so the card may need a poll to appear.
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
  await page.goto(`/services/${imageResizer}/task-definition`)

  // Same 512 MiB memory: the fake estate OOM-kills image-resizer's new revision.
  await page.getByRole('button', { name: 'Add variable' }).click()
  await page.getByLabel('Name').last().fill('X')
  await page.getByLabel('Value').last().fill('1')
  await registerAndDeploy(page, 7)

  await expect(page.getByText('image-resizer: revision :7 failed to start. Rolled back to :6.')).toBeVisible({ timeout: 20_000 })

  await openOperationsLog(page, imageResizer)
  await expect(async () => {
    await page.reload()
    await expect(page.getByText('Deployment of image-resizer:7 failed: ECS deployment circuit breaker: tasks failed to start.')).toBeVisible({
      timeout: 2_000,
    })
  }).toPass({ timeout: 15_000 })
})

test('stop a task', async ({ page }) => {
  await login(page, 'pavel')
  await page.goto(`/services/${paymentsApi}/tasks`)

  await page.getByRole('button', { name: 'Stop', exact: true }).first().click()
  const dialog = page.getByRole('dialog')
  const title = (await dialog.getByRole('heading').first().textContent()) ?? ''
  const shortId = /^Stop task (\S+)\?$/.exec(title.trim())?.[1]
  expect(shortId, `short task id in dialog title "${title}"`).toBeTruthy()
  await dialog.getByRole('button', { name: 'Stop task' }).click()

  // The dialog shows the full task id; the table shows its first 12 characters.
  const row = page.getByRole('row').filter({ hasText: shortId!.slice(0, 12) })
  await expect(row.getByText('Stopped', { exact: true })).toBeVisible({ timeout: 10_000 })

  await openOperationsLog(page, paymentsApi)
  await expect(page.getByText(/Stopped task /).first()).toBeVisible()
})
