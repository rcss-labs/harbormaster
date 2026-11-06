import { asUser, expect, test, type Page } from './fixtures/test'
import { SVC, serviceUrl } from './fixtures/ids'
import { pickImageTag, reviewAndApply, serviceId } from './helpers'

// The Deploy tab: containers, task size, service settings and auto scaling reviewed as one change set. Every test
// here mutates the shared estate, so the file runs in chromium-flows (after change.spec, which moves payments-api
// to :43 and leaves image-resizer rolled back to :6) and uses services no other flow spec depends on, except where
// noted. Revision numbers are read from the API instead of being hard-coded, so the order of files does not matter.
test.describe.configure({ mode: 'serial' })

const HOMELAB = '482109375512'
const paymentsApi = serviceUrl(...SVC.paymentsApi, 'deploy')
const imageResizer = serviceUrl(...SVC.imageResizer, 'deploy')
const stagingApi = serviceUrl(...SVC.stagingApi, 'deploy')
// Services only this file touches (no other flow spec reads them): a Dev service for exec, a Prod one for rollout
// settings and a Dev one without auto scaling for the Operate user.
const emailSender = ['482109375512', 'us-east-1', 'background-jobs', 'email-sender'] as const
const authService = ['482109375512', 'us-east-1', 'core-services', 'auth-service'] as const
const searchIndexer = [HOMELAB, 'eu-west-1', 'eu-apps', 'search-indexer'] as const
// The staging-web cluster's `web` service (Staging tag).
const stagingWeb = ['731022849906', 'us-east-1', 'staging-web', 'web'] as const

interface Settings {
  deployed_revision: number
  enable_execute_command: boolean
  deployment_configuration: { circuit_breaker: { enable: boolean; rollback: boolean }; minimum_healthy_percent: number; maximum_percent: number }
  health_check_grace_period_seconds: number | null
}

async function readSettings(page: Page, id: string): Promise<Settings> {
  const response = await page.request.get(`/api/services/${id}/deploy-settings`)
  expect(response.ok()).toBe(true)
  return (await response.json()) as Settings
}

async function revisionCount(page: Page, id: string): Promise<number> {
  const response = await page.request.get(`/api/services/${id}/revisions`)
  expect(response.ok()).toBe(true)
  return ((await response.json()) as { items: unknown[] }).items.length
}

/** Resolves the revision N from the review dialog's primary button ("Register and deploy :N"), then presses it. */
async function applyRegisterAndDeploy(page: Page, serviceName: string, reason?: string): Promise<number> {
  await page.getByRole('button', { name: 'Review…' }).click()
  const review = page.getByRole('dialog')
  await expect(review.getByRole('heading', { name: `Review changes to ${serviceName}` })).toBeVisible()
  if (reason !== undefined) await review.getByRole('textbox', { name: 'Reason' }).fill(reason)
  const apply = review.getByRole('button', { name: /^Register and deploy :\d+$/ })
  const revision = Number(/:(\d+)$/.exec((await apply.textContent()) ?? '')?.[1])
  await apply.click()
  await expect(page.getByText(`Deploying ${serviceName} to :${revision}.`)).toBeVisible()
  return revision
}

async function waitForRolloutDone(page: Page) {
  const progress = page.getByRole('region', { name: 'Rollout progress' })
  await expect(progress.getByText('Completed')).toBeVisible({ timeout: 40_000 })
}

test.describe('sam on payments-api', () => {
  test.use(asUser('sam'))

  test('deploys an older tag found by search plus a new env var; progress shows from→to and the operations log carries the reason', async ({ page }) => {
    const id = serviceId(...SVC.paymentsApi)
    const from = (await readSettings(page, id)).deployed_revision
    const reason = 'Pin the provider SDK back until the ledger bug is fixed'

    await page.goto(paymentsApi)
    // The list holds the 10 newest tags; 1.16.4 is only reachable through the server-side search.
    await page.getByRole('combobox', { name: 'Image tag' }).click()
    const tags = page.getByRole('listbox', { name: 'Image tags' })
    await expect(tags.getByRole('option')).toHaveCount(10)
    await expect(tags).toContainText('10 of 24 tags')
    await pickImageTag(page, '1.16.4', { search: '1.16' })
    await page.getByRole('button', { name: 'Add variable' }).click()
    await page.getByRole('textbox', { name: 'Variable name 4' }).fill('FEATURE_NEW_LEDGER')
    await page.getByRole('textbox', { name: 'Variable value 4' }).fill('off')
    const pending = page.getByRole('region', { name: 'Pending changes' })
    await expect(pending.getByText('1 change')).toBeVisible()
    // The aside shows each edited field live, before and after.
    await expect(pending.getByRole('listitem', { name: 'Image tag' })).toContainText('→ 1.16.4')
    await expect(pending.getByRole('listitem', { name: 'Environment FEATURE_NEW_LEDGER' })).toContainText('Environment FEATURE_NEW_LEDGER: Not set → off')

    await page.getByRole('button', { name: 'Review…' }).click()
    const review = page.getByRole('dialog')
    const taskDefinition = review.getByRole('region', { name: 'Task definition changes' })
    await expect(taskDefinition.getByText(/1\.16\.4/)).toBeVisible()
    await expect(taskDefinition.getByText(/FEATURE_NEW_LEDGER/).first()).toBeVisible()
    await review.getByRole('button', { name: 'Keep editing' }).click()

    const to = await applyRegisterAndDeploy(page, 'payments-api', reason)
    expect(to).toBeGreaterThan(from)

    // The footer resets and the progress panel shows the move once the 5 s poll sees the new deployment.
    const progress = page.getByRole('region', { name: 'Rollout progress' })
    await expect(progress).toContainText(`:${from} → :${to}`, { timeout: 15_000 })
    await expect(page.getByRole('region', { name: 'Pending changes' }).getByText('0 changes')).toBeVisible()
    await waitForRolloutDone(page)

    // The registered revision carries the edits.
    await page.goto(serviceUrl(...SVC.paymentsApi, 'task-definition'))
    const editor = page.getByRole('textbox', { name: 'Task definition JSON' })
    await expect(editor).toHaveValue(/FEATURE_NEW_LEDGER/)
    await expect(editor).toHaveValue(/payments-api:1\.16\.4/)

    // The reason travels with the change set and is shown on the operations lines it produced.
    await page.goto(serviceUrl(...SVC.paymentsApi, 'operations'))
    await expect(page.getByText(`Deployed payments-api:${to}.`)).toBeVisible()
    await expect(page.getByText(reason).first()).toBeVisible()
  })

  test('the Deploy label is bold and no other tab label is', async ({ page }) => {
    await page.goto(paymentsApi)
    const tabs = page.locator('nav', { has: page.getByRole('link', { name: 'History', exact: true }) })
    const deploy = tabs.getByRole('link', { name: /^Deploy/ })
    await expect(deploy.locator('strong')).toHaveText('Deploy')
    await expect(deploy.locator('strong')).toHaveCSS('font-weight', /^(600|700|800|900)$/)
    await expect(tabs.getByRole('link').locator('strong')).toHaveCount(1)
    await expect(tabs.getByRole('link', { name: 'History', exact: true }).locator('strong')).toHaveCount(0)
  })
})

test.describe('sam on a phone', () => {
  test.use(asUser('sam'))

  test('Deploy never scrolls sideways at 390px and the pending aside stacks below the form', async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 800 })
    await page.goto(paymentsApi)
    const aside = page.getByRole('region', { name: 'Pending changes' })
    await expect(aside).toBeVisible()
    const service = page.getByRole('region', { name: 'Service' })
    await expect(service.getByRole('spinbutton', { name: 'Desired count' })).toBeVisible()
    expect((await aside.boundingBox())!.y).toBeGreaterThan((await service.boundingBox())!.y)
    await expect(page.getByRole('button', { name: 'Add variable' })).toBeVisible()
    await expect(page.getByRole('table', { name: 'Scaling policies' }).or(page.getByRole('grid', { name: 'Scaling policies' }))).toBeVisible()
    // Every Deploy panel, the aside and the page itself stay inside the window. (The header's tab strip is the page
    // frame's and is checked with the service page.)
    const overflow = await page.evaluate(() => {
      const limit = document.documentElement.clientWidth
      const wide = Array.from(document.querySelectorAll('main section, main aside')).filter((element) => element.getBoundingClientRect().right > limit + 0.5)
      return { page: document.documentElement.scrollWidth - limit, wide: wide.map((element) => element.getAttribute('aria-label') ?? element.tagName) }
    })
    expect(overflow.page).toBeLessThanOrEqual(0)
    expect(overflow.wide).toEqual([])
  })
})

test.describe('sam on staging api', () => {
  test.use(asUser('sam'))

  test('edits the migrator sidecar image and the task size in one change set', async ({ page }) => {
    await page.goto(stagingApi)
    await expect(page.getByRole('group', { name: 'Container' }).getByRole('button')).toHaveText(['api', 'migrator'])

    // The sidecar is an ECR image too: its own six tags, searched and picked per container.
    await pickImageTag(page, '1.4.4', { container: 'migrator' })
    await page.getByLabel('Task memory').selectOption('2048')
    await expect(page.getByRole('region', { name: 'Pending changes' }).getByText('1 change')).toBeVisible()

    const revision = await applyRegisterAndDeploy(page, 'api')
    await waitForRolloutDone(page)

    // Switching containers shows the saved sidecar tag; the main container is untouched.
    await page.reload()
    await expect(page.getByLabel('Task memory')).toHaveValue('2048')
    await expect(page.getByRole('combobox', { name: 'Image tag' })).toHaveValue('1.19.0-rc.2')
    await page.getByRole('group', { name: 'Container' }).getByRole('button', { name: 'migrator', exact: true }).click()
    await expect(page.getByRole('combobox', { name: 'Image tag' })).toHaveValue('1.4.4')

    await page.goto(serviceUrl(...SVC.stagingApi, 'task-definition'))
    await expect(page.getByRole('row').filter({ hasText: `:${revision}` })).toContainText('Deployed')
  })
})

test.describe('omer on search-indexer (Operate on Dev)', () => {
  test.use(asUser('omer'))

  test('adds auto scaling with a CPU policy and a scheduled action; the Scaling tab shows them', async ({ page }) => {
    const url = serviceUrl(...searchIndexer, 'deploy')
    await page.goto(url)
    // An Operate-only user edits scaling but not the containers.
    await expect(page.getByRole('combobox', { name: 'Image tag' })).toBeDisabled()
    await expect(page.getByRole('button', { name: 'Add auto scaling target' })).toBeEnabled()

    await page.getByRole('button', { name: 'Add auto scaling target' }).click()
    await page.getByRole('spinbutton', { name: 'Minimum', exact: true }).fill('1')
    await page.getByRole('spinbutton', { name: 'Maximum', exact: true }).fill('3')

    await page.getByRole('button', { name: 'Add policy' }).click()
    const policy = page.getByRole('form', { name: 'New scaling policy' })
    await policy.getByLabel('Policy name').fill('cpu-70')
    await policy.getByLabel('Target value').fill('70')
    await policy.getByRole('button', { name: 'Save policy' }).click()
    await expect(page.getByRole('table', { name: 'Scaling policies' }).or(page.getByRole('grid', { name: 'Scaling policies' }))).toContainText('cpu-70')

    await page.getByRole('button', { name: 'Add scheduled action' }).click()
    const action = page.getByRole('form', { name: 'New scheduled action' })
    await action.getByLabel('Name', { exact: true }).fill('office-hours')
    await action.getByLabel('Cron fields').fill('0 7 * * ? *')
    await action.getByLabel('Minimum capacity').fill('2')
    await action.getByRole('button', { name: 'Save action' }).click()

    await reviewAndApply(page, 'search-indexer', 'Apply changes')
    await expect(page.getByText('Applied the changes to search-indexer.')).toBeVisible()

    await page.goto(serviceUrl(...searchIndexer, 'scaling'))
    const valueOf = (term: string) => page.getByRole('term').filter({ hasText: new RegExp(`^${term}$`) }).locator('xpath=following-sibling::dd[1]')
    await expect(valueOf('Minimum tasks')).toHaveText('1')
    await expect(valueOf('Maximum tasks')).toHaveText('3')
    await expect(page.getByText(/CPU.*70/).first()).toBeVisible()
    await expect(page.getByText(/office-hours|0 7 \* \* \? \*/).first()).toBeVisible()
  })
})

test.describe('sam on email-sender', () => {
  test.use(asUser('sam'))

  test('turning ECS Exec off restarts the service with a new deployment, without a new revision', async ({ page }) => {
    const id = serviceId(...emailSender)
    const before = await readSettings(page, id)
    expect(before.enable_execute_command).toBe(true)
    const revisions = await revisionCount(page, id)

    await page.goto(serviceUrl(...emailSender, 'deploy'))
    await expect(page.getByText(/Applies to new tasks only/)).toBeVisible()
    await page.getByRole('checkbox', { name: 'ECS Exec' }).uncheck()
    await page.getByRole('button', { name: 'Review…' }).click()
    const review = page.getByRole('dialog')
    await expect(review.getByRole('region', { name: 'Service changes' }).getByText('ECS Exec')).toBeVisible()
    await expect(review.getByRole('region', { name: 'Task definition changes' })).toHaveCount(0)
    await review.getByRole('button', { name: 'Apply changes' }).click()
    await expect(page.getByText(/^(Deploying email-sender to :\d+|Applied the changes to email-sender)\.$/)).toBeVisible()

    // A fresh deployment of the same revision starts; the setting is off and no revision was registered.
    await expect(page.getByRole('region', { name: 'Rollout progress' })).toBeVisible({ timeout: 15_000 })
    const after = await readSettings(page, id)
    expect(after.enable_execute_command).toBe(false)
    expect(after.deployed_revision).toBe(before.deployed_revision)
    expect(await revisionCount(page, id)).toBe(revisions)

    await page.goto(serviceUrl(...emailSender, 'operations'))
    await expect(page.getByText(/ECS Exec/i).first()).toBeVisible()
    await expect(page.getByText(/force new deployment|Restarted service/i).first()).toBeVisible()
  })
})

test.describe('sam on auth-service', () => {
  test.use(asUser('sam'))

  test('changes only rollout settings (circuit breaker, percentages, grace period) and registers no revision', async ({ page }) => {
    const id = serviceId(...authService)
    const before = await readSettings(page, id)
    const revisions = await revisionCount(page, id)

    await page.goto(serviceUrl(...authService, 'deploy'))
    await page.getByRole('checkbox', { name: 'Circuit breaker' }).check()
    await page.getByRole('checkbox', { name: 'Roll back on failure' }).check()
    await page.getByRole('spinbutton', { name: 'Minimum healthy percent' }).fill('50')
    await page.getByRole('spinbutton', { name: 'Maximum percent' }).fill('150')
    await page.getByRole('spinbutton', { name: 'Health check grace period (seconds)' }).fill('90')

    await page.getByRole('button', { name: 'Review…' }).click()
    const review = page.getByRole('dialog')
    await expect(review.getByRole('region', { name: 'Task definition changes' })).toHaveCount(0)
    await expect(review.getByRole('region', { name: 'Service changes' })).toContainText('90')
    await review.getByRole('button', { name: 'Apply changes' }).click()
    await expect(page.getByText('Applied the changes to auth-service.')).toBeVisible()

    await expect(async () => {
      const after = await readSettings(page, id)
      expect(after.deployment_configuration.circuit_breaker).toEqual({ enable: true, rollback: true })
      expect(after.deployment_configuration.minimum_healthy_percent).toBe(50)
      expect(after.deployment_configuration.maximum_percent).toBe(150)
      expect(after.health_check_grace_period_seconds).toBe(90)
      expect(after.deployed_revision).toBe(before.deployed_revision)
    }).toPass({ timeout: 10_000 })
    expect(await revisionCount(page, id)).toBe(revisions)
  })
})

// A partial run (register succeeded, a later step failed, then "Retry remaining steps" without registering again)
// needs the fake AWS to fail one specific call, which only Go tests can arrange (Fake.FailNext). A real e2e path would
// need a test-only backdoor in the server. It is covered by deploy.TestChangeSet_* (UpdateServiceFailureAfterRegisterIsPartial,
// RetryWithRevisionDoesNotRegisterAgain), the API test _PartialIs200WithSteps and DeployTab.review.test.tsx.
test.skip('partial failure shows the steps and retries without registering again', () => {})

test.describe('sam on image-resizer (failing OOM rollout)', () => {
  test.use(asUser('sam'))

  test('one-click rollback during the failing rollout returns the service to the earlier revision', async ({ page }) => {
    const id = serviceId(...SVC.imageResizer)
    // change.spec leaves its own failed rollout behind; the change set is refused while one is running.
    await expect(async () => {
      const response = await page.request.get(`/api/services/${id}`)
      const service = (await response.json()) as { deployments: { rollout_state: string }[] }
      expect(service.deployments.some((deployment) => deployment.rollout_state === 'IN_PROGRESS')).toBe(false)
    }).toPass({ timeout: 40_000 })
    const earlier = (await readSettings(page, id)).deployed_revision

    await page.goto(imageResizer)
    await page.getByRole('button', { name: 'Add variable' }).click()
    await page.getByRole('textbox', { name: 'Variable name 2' }).fill('Y')
    await page.getByRole('textbox', { name: 'Variable value 2' }).fill('2')
    const failing = await applyRegisterAndDeploy(page, 'image-resizer')
    expect(failing).toBeGreaterThan(earlier)

    // The new revision is OOM-killed, so tasks keep failing to start: the panel offers the way back.
    const progress = page.getByRole('region', { name: 'Rollout progress' })
    const rollback = progress.getByRole('button', { name: `Roll back to :${earlier}` })
    await expect(rollback).toBeVisible({ timeout: 15_000 })
    await rollback.click()
    const confirm = page.getByRole('dialog')
    await expect(confirm.getByRole('heading', { name: `Roll back image-resizer to :${earlier}?` })).toBeVisible()
    await confirm.getByRole('button', { name: `Roll back to :${earlier}` }).click()
    await expect(page.getByRole('dialog')).toHaveCount(0)

    await expect(progress.getByText(`image-resizer:${earlier}`)).toBeVisible({ timeout: 20_000 })
    await waitForRolloutDone(page)
    expect((await readSettings(page, id)).deployed_revision).toBe(earlier)
  })
})

test.describe('omer on staging web (Viewer only)', () => {
  test.use(asUser('omer'))

  test('the Deploy tab is locked and shows no editors', async ({ page }) => {
    await page.goto(serviceUrl(...stagingWeb, 'deploy'))
    const tabs = page.locator('nav', { has: page.getByRole('link', { name: 'History', exact: true }) })
    const deploy = tabs.getByRole('link', { name: /^Deploy/ })
    // Locked tabs are marked with ⊘ and keep the bold label.
    await expect(deploy).toContainText('⊘')
    await expect(deploy.locator('strong')).toHaveText('Deploy')
    await expect(page.getByText(/You need\s+.*\s+on\s+Staging\s+to use this\./)).toBeVisible()
    await expect(page.getByRole('region', { name: 'Pending changes' })).toHaveCount(0)
    await expect(page.getByRole('combobox', { name: 'Image tag' })).toHaveCount(0)
  })
})
