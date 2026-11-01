import { asUser, expect, test, type Page } from './fixtures/test'
import { SVC, serviceUrl } from './fixtures/ids'
import { serviceId } from './helpers'

// Role-based complement to change.spec (admin register/deploy/rollback, operator read-only, stop task) and
// terminal.spec (admin terminal, exec-off banner): one describe per Service page tab, each signed in as the
// role whose permissions make that tab interesting. The Tasks tab check on the unmutated seed is in
// service-observe.spec.ts (chromium-observe), because change.spec's rollback leaves image-resizer with running tasks. Several describes mutate the shared estate (restart,
// register :22, desired count, a terminal session), so this file runs in the chromium-flows project, after
// change.spec (alphabetical order). Assertions avoid revisions and counts that change.spec moves.
test.describe.configure({ mode: 'serial' })

const paymentsApi = (tab?: string) => serviceUrl(...SVC.paymentsApi, tab)
const imageResizer = (tab?: string) => serviceUrl(...SVC.imageResizer, tab)
const jenkins = (tab?: string) => serviceUrl(...SVC.jenkins, tab)
const stagingApi = (tab?: string) => serviceUrl(...SVC.stagingApi, tab)
const edgeNginx = (tab?: string) => serviceUrl(...SVC.edgeNginx, tab)
// The staging-web cluster's `web` service (Staging tag).
const stagingWeb = (tab?: string) => serviceUrl('731022849906', 'us-east-1', 'staging-web', 'web', tab)

/** Replaces the whole JSON document in the task definition editor (the textarea is controlled, so fill it). */
async function editDefinitionJson(page: Page, change: (json: string) => string) {
  const editor = page.getByLabel('Task definition JSON')
  await expect(editor).toBeEditable()
  const current = await editor.inputValue()
  const next = change(current)
  expect(next, 'the edit must change the JSON').not.toEqual(current)
  await editor.fill(next)
}

/** Waits for the Revisions table to load (its deployed row), then counts the table rows. */
async function revisionRowCount(page: Page) {
  await expect(page.getByRole('cell', { name: 'Deployed', exact: true })).toBeVisible()
  return page.getByRole('row').count()
}

test.describe('header: Prod viewer', () => {
  test.use(asUser('dana'))

  test('Restart and Desired count are disabled on Prod without Operate; AWS console link is present', async ({ page }) => {
    await page.goto(paymentsApi())
    const restart = page.getByRole('button', { name: 'Restart (new deployment)' })
    await expect(restart).toBeDisabled()
    await expect(restart).toHaveAttribute('title', 'You need Operate on Prod')
    const desired = page.getByRole('button', { name: 'Desired count: 3' })
    await expect(desired).toBeDisabled()
    await expect(desired).toHaveAttribute('title', 'You need Operate on Prod')
    await expect(page.getByRole('link', { name: /AWS console/ })).toHaveAttribute('href', /.+/)
  })
})

test.describe('overview as oncall', () => {
  test.use(asUser('oncall'))

  test('payments-api shows 3 running of 3, the target health donut and the deployments timeline', async ({ page }) => {
    await page.goto(paymentsApi())
    // change.spec stops a payments-api task; ECS replaces it within a few polls.
    await expect(page.getByText('running of 3 · 0 pending')).toBeVisible({ timeout: 30_000 })
    await expect(page.getByRole('heading', { name: 'Target health' })).toBeVisible()
    await expect(page.getByRole('img').filter({ hasText: '3/3' })).toBeVisible()
    await expect(page.getByRole('heading', { name: 'Deployments, 14 days' })).toBeVisible()
  })
})

test.describe('metrics as pavel', () => {
  test.use(asUser('pavel'))

  test('edge-nginx (Container Insights off) shows the Terraform banner and no charts', async ({ page }) => {
    await page.goto(edgeNginx('metrics'))
    await expect(page.getByText(/Container Insights is off for edge-proxy/)).toBeVisible()
    await expect(page.getByText(/Terraform/)).toBeVisible()
    await expect(page.getByRole('heading', { name: 'CPU utilization' })).toHaveCount(0)
    await expect(page.getByRole('group', { name: 'Range' })).toHaveCount(0)
  })

  test('payments-api shows the four ranges and four chart panels', async ({ page }) => {
    await page.goto(paymentsApi('metrics'))
    const range = page.getByRole('group', { name: 'Range' })
    await expect(range.getByRole('button')).toHaveText(['1h', '6h', '24h', '72h'])
    for (const title of ['CPU utilization', 'Memory utilization', 'Network', 'Running task count']) {
      await expect(page.getByRole('heading', { name: title })).toBeVisible()
    }
    await range.getByRole('button', { name: '72h' }).click()
    await expect(page).toHaveURL(/range=72h/)
    await expect(range.getByRole('button', { name: '72h' })).toHaveAttribute('aria-pressed', 'true')
  })
})

test.describe('task definition: read-only Prod viewer', () => {
  test.use(asUser('dana'))

  test('payments-api (no Edit on Prod) is read only', async ({ page }) => {
    await page.goto(paymentsApi('task-definition'))
    await expect(page.getByText(/Read only\. You need\s+Edit task definition\s+on\s+Prod\s+to register a new revision\./)).toBeVisible()
    await expect(page.getByLabel('Task definition JSON')).not.toBeEditable()
    await expect(page.getByLabel('Image tag')).toBeDisabled()
    await expect(page.getByRole('button', { name: 'Review and save' })).toBeDisabled()
  })
})

test.describe('task definition: registering', () => {
  test.describe('dana on staging api (Deployer)', () => {
    test.use(asUser('dana'))

    test('registers :22 from quick edit, declines the deploy prompt and sees the pending banner', async ({ page }) => {
      await page.goto(stagingApi('task-definition'))
      const revisionsBefore = await revisionRowCount(page)

      const imageTag = page.getByLabel('Image tag')
      if ((await imageTag.evaluate((element) => element.tagName)) === 'SELECT') {
        // Pick any tag other than the current one.
        const options = await imageTag.locator('option').evaluateAll((all) => all.map((option) => (option as HTMLOptionElement).value))
        const current = await imageTag.inputValue()
        await imageTag.selectOption(options.find((value) => value !== current) ?? current)
      } else {
        await imageTag.fill('1.19.0-rc.3')
      }
      await page.getByRole('button', { name: 'Review and save' }).click()
      const review = page.getByRole('dialog')
      await expect(review.getByText('Register api:22?')).toBeVisible()
      await review.getByRole('button', { name: 'Register revision :22' }).click()
      const prompt = page.getByRole('dialog')
      await expect(prompt.getByText('Revision :22 is registered. Deploy it now?')).toBeVisible()
      await prompt.getByRole('button', { name: 'Not now' }).click()
      await expect(page.getByRole('dialog')).toHaveCount(0)

      // The service header offers to deploy the registered revision.
      await expect(page.getByRole('button', { name: 'Deploy :22' })).toBeVisible()
      const row = page.getByRole('row').filter({ hasText: ':22' })
      await expect(row).toContainText('Not deployed')
      await expect(row).toContainText('dana')
      await expect(page.getByRole('row')).toHaveCount(revisionsBefore + 1)
    })
  })

  test.describe('dana on jenkins-controller (Dev Full, not a global admin)', () => {
    test.use(asUser('dana'))

    test('changing the task role is refused: Changing roles needs a global admin', async ({ page }) => {
      await page.goto(jenkins('task-definition'))
      const revisionsBefore = await revisionRowCount(page)

      await editDefinitionJson(page, (json) => json.replace(/("taskRoleArn":\s*"[^"]*)"/, '$1-other"'))
      await page.getByRole('button', { name: 'Review and save' }).click()
      const review = page.getByRole('dialog')
      await review.getByRole('button', { name: 'Register revision :59' }).click()
      await expect(review.getByText('Changing roles needs a global admin')).toBeVisible()

      await review.getByRole('button', { name: 'Keep editing' }).click()
      await expect(page.getByRole('row')).toHaveCount(revisionsBefore)
      await expect(page.getByRole('row').filter({ hasText: ':59' })).toHaveCount(0)
    })
  })

  test.describe('pavel on payments-api (global admin)', () => {
    test.use(asUser('pavel'))

    test('changing the family is refused: Use Terraform to create a new family', async ({ page }) => {
      await page.goto(paymentsApi('task-definition'))
      await editDefinitionJson(page, (json) => json.replace(/("family":\s*"payments-api)"/, '$1-copy"'))
      await page.getByRole('button', { name: 'Review and save' }).click()
      const review = page.getByRole('dialog')
      // change.spec may already have registered :43, so the number is not fixed here.
      await review.getByRole('button', { name: /^Register revision :\d+$/ }).click()
      await expect(review.getByText('Use Terraform to create a new family')).toBeVisible()
    })
  })
})

test.describe('scaling', () => {
  test.describe('omer on image-resizer (no auto scaling)', () => {
    test.use(asUser('omer'))

    test('sets the desired count to 3 from the tab, without an auto scaling warning', async ({ page }) => {
      await page.goto(imageResizer('scaling'))
      await expect(page.getByText(/No auto scaling: the count stays where you set it\./)).toBeVisible()
      await page.getByLabel('Desired count', { exact: true }).fill('3')
      await expect(page.getByText(/Outside the auto scaling range/)).toHaveCount(0)
      await page.getByRole('button', { name: 'Update desired count' }).click()
      await expect(page.getByRole('button', { name: 'Desired count: 3' })).toBeVisible({ timeout: 10_000 })

      // Put the estate back so later specs see the seeded count.
      await page.getByLabel('Desired count', { exact: true }).fill('2')
      await page.getByRole('button', { name: 'Update desired count' }).click()
      await expect(page.getByRole('button', { name: 'Desired count: 2' })).toBeVisible({ timeout: 10_000 })
    })
  })

  test.describe('dana on payments-api (Prod, no Operate)', () => {
    test.use(asUser('dana'))

    test('shows the auto scaling range and target read-only and cannot update the count', async ({ page }) => {
      await page.goto(paymentsApi('scaling'))
      await expect(page.getByRole('button', { name: 'Update desired count' })).toBeDisabled()
      const rangeValue = (label: string) => page.getByText(label, { exact: true }).locator('xpath=following-sibling::dd[1]')
      await expect(rangeValue('Minimum tasks')).toHaveText('2')
      await expect(rangeValue('Maximum tasks')).toHaveText('6')
      await expect(page.getByText(/at 60%/)).toBeVisible()
    })
  })
})

test.describe('logs', () => {
  test.describe('dana on payments-api (Logs on Prod)', () => {
    test.use(asUser('dana'))

    test('lists lines, both containers, and the warnings-and-errors filter reduces the rows', async ({ page }) => {
      await page.goto(paymentsApi('logs'))
      const log = page.getByRole('log')
      const healthLines = log.getByText('GET /health 200')
      await expect(healthLines.first()).toBeVisible({ timeout: 15_000 })

      const container = page.getByLabel('Container')
      await expect(container.locator('option', { hasText: 'payments-api' })).toHaveCount(1)
      await expect(container.locator('option', { hasText: 'otel-collector' })).toHaveCount(1)

      const rowCount = () => log.evaluate((element) => element.children.length)
      const allRows = await rowCount()
      await page.getByLabel('Level').selectOption({ label: 'Warnings and errors' })
      await expect(healthLines).toHaveCount(0)
      await expect.poll(rowCount).toBeLessThan(allRows)
    })
  })

  test.describe('omer on staging web (Viewer only)', () => {
    test.use(asUser('omer'))

    test('the Logs tab is locked: marker, lock panel and no log lines', async ({ page }) => {
      await page.goto(stagingWeb('logs'))
      await expect(page.getByRole('link', { name: 'Logs ⊘' })).toBeVisible()
      await expect(
        page.getByText(/You need\s+Logs\s+on\s+Staging\s+to use this\. Ask a global admin to change your permissions\./),
      ).toBeVisible()
      await expect(page.getByRole('log')).toHaveCount(0)
    })
  })
})

test.describe('terminal', () => {
  test.describe('dana on jenkins-controller (Dev Full)', () => {
    test.use(asUser('dana'))

    test('connects over the WebSocket, runs hostname, disconnects and logs open and close', async ({ page }) => {
      await page.goto(jenkins('terminal'))
      await expect(page.getByLabel('Task')).toBeVisible()
      await expect(page.getByLabel('Container')).toHaveValue('jenkins-controller', { timeout: 15_000 })
      await expect(page.getByLabel('Command')).toHaveValue('/bin/sh')
      await page.getByRole('button', { name: 'Connect' }).click()

      // xterm renders to the DOM without roles; its row container and helper textarea are the only handles.
      const screen = page.locator('.xterm-rows')
      await expect(screen).toContainText('Starting session with SessionId: ecs-execute-command-', { timeout: 15_000 })
      await page.getByLabel('Terminal input').focus()
      await page.keyboard.type('hostname')
      await page.keyboard.press('Enter')
      await expect(screen).toContainText('ip-10-20-1-149.ec2.internal')

      await page.getByRole('button', { name: 'Disconnect' }).click()
      await expect(screen).toContainText('Exiting session with sessionId')

      await page.goto(jenkins('operations'))
      await expect(page.getByText(/^Opened terminal session on task [0-9a-f]+ \(jenkins-controller, \/bin\/sh\)\.$/).first()).toBeVisible()
      await expect(page.getByText(/^Closed terminal session on task [0-9a-f]+ \(jenkins-controller\) after /).first()).toBeVisible()
    })
  })

  test.describe('oncall on payments-api (Prod Terminal)', () => {
    test.use(asUser('oncall'))

    test('Connect is enabled', async ({ page }) => {
      await page.goto(paymentsApi('terminal'))
      await expect(page.getByRole('button', { name: 'Connect' })).toBeEnabled({ timeout: 15_000 })
    })
  })

  test.describe('dana on payments-api (Prod, no Terminal)', () => {
    test.use(asUser('dana'))

    test('the Terminal tab is locked with the lock panel', async ({ page }) => {
      await page.goto(paymentsApi('terminal'))
      await expect(page.getByRole('link', { name: 'Terminal ⊘' })).toBeVisible()
      await expect(
        page.getByText(/You need\s+Terminal\s+on\s+Prod\s+to use this\. Ask a global admin to change your permissions\./),
      ).toBeVisible()
      await expect(page.getByRole('button', { name: 'Connect' })).toHaveCount(0)
    })
  })
})

test.describe('operations log as pavel', () => {
  test.use(asUser('pavel'))

  test('is newest first, and the toggle hides ECS service events', async ({ page }) => {
    const response = await page.request.get(`/api/services/${serviceId(...SVC.paymentsApi)}/operations?include_ecs=true`)
    expect(response.ok()).toBe(true)
    const { items } = (await response.json()) as { items: { ts: string }[] }
    const stamps = items.map((item) => Date.parse(item.ts))
    expect(stamps.length).toBeGreaterThan(1)
    expect(stamps).toEqual([...stamps].sort((a, b) => b - a))

    await page.goto(paymentsApi('operations'))
    const ecsRows = page.getByText('ECS event', { exact: true })
    await expect(ecsRows.first()).toBeVisible()
    await page.getByLabel('Show ECS service events').uncheck()
    await expect(ecsRows).toHaveCount(0)
  })
})

test.describe('service the user cannot see', () => {
  test.use(asUser('omer'))

  test('omer on payments-api gets No access in the UI and 404 from the API', async ({ page }) => {
    const id = serviceId(...SVC.paymentsApi)
    const response = await page.request.get(`/api/services/${id}`)
    expect(response.status()).toBe(404)

    await page.goto(paymentsApi())
    await expect(page.getByRole('heading', { name: 'No access' })).toBeVisible()
    await expect(page.getByRole('heading', { name: 'payments-api', level: 1 })).toHaveCount(0)
  })
})

test.describe('header: Dev operator', () => {
  test.use(asUser('omer'))

  test('Restart on image-resizer confirms in a dialog and lands in the operations log', async ({ page }) => {
    await page.goto(imageResizer())
    // change.spec's OOM rollback leaves a rollout open for a while, which disables Restart; wait it out.
    const restart = page.getByRole('button', { name: 'Restart (new deployment)' })
    await expect(restart).toBeEnabled({ timeout: 30_000 })
    await restart.click()
    const dialog = page.getByRole('dialog')
    await expect(dialog.getByText('Restart image-resizer?')).toBeVisible()
    await dialog.getByRole('button', { name: 'Restart', exact: true }).click()
    // Navigate only once the request has been accepted, or the page change cancels it.
    await expect(page.getByText('Restart started.')).toBeVisible()

    await page.goto(imageResizer('operations'))
    await expect(page.getByText('Restarted service (force new deployment) on revision 6.').first()).toBeVisible()
  })
})
