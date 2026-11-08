import { asUser, expect, test } from './fixtures/test'
import { SVC, serviceUrl } from './fixtures/ids'
import { pickImageTag } from './helpers'

// Image security end to end against the fake scanner: the button, the pre-deploy gate and who sees what on /security.
// The file mutates the estate (it deploys edge-nginx), so it must stay out of the read-only `*security.spec.ts` project:
// this name falls into chromium-flows. payments-api keeps its setting off, so change.spec and deploy.spec are not gated.
test.describe.configure({ mode: 'serial' })

const stagingWeb = serviceUrl('731022849906', 'us-east-1', 'staging-web', 'web', 'deploy')
const paymentsApi = serviceUrl(...SVC.paymentsApi, 'deploy')
const edgeNginx = serviceUrl(...SVC.edgeNginx, 'deploy')

// Fake tags of edge-nginx: 1.27.4 and 1.27.3 are newer than the running 1.27.2 (clean); 1.27.1 is older and carries
// the same findings as the running image, one of them critical.
const VULNERABLE_TAG = '1.27.1'

test.describe('dana scans a Staging image on request', () => {
  test.use(asUser('dana'))

  test('the button scans the current tag with the setting off, and /security lists it', async ({ page }) => {
    await page.goto(stagingWeb)
    await expect(page.getByRole('button', { name: 'Scan for vulnerabilities' })).toBeEnabled()
    await page.getByRole('button', { name: 'Scan for vulnerabilities' }).click()
    await expect(page.getByText(/^Scanned .*: 0 critical/)).toBeVisible({ timeout: 30_000 })

    await page.goto('/security')
    await expect(page.getByRole('heading', { name: 'Image security' })).toBeVisible()
    await expect(page.getByText("Only images requested from a service's Deploy tab are scanned.")).toBeVisible()
    await expect(page.getByRole('row', { name: /web/ }).getByText('Scanned')).toBeVisible()
  })
})

test.describe('sam requests a Prod scan', () => {
  test.use(asUser('sam'))

  test('payments-api is scanned on request without any deploy', async ({ page }) => {
    await page.goto(paymentsApi)
    await page.getByRole('button', { name: 'Scan for vulnerabilities' }).click()
    await expect(page.getByText(/^Scanned .*: 1 critical/)).toBeVisible({ timeout: 30_000 })
  })
})

test.describe('sam deploys through the gate', () => {
  test.use(asUser('sam'))

  test('a vulnerable older tag pauses at the gate, Deploy anyway rolls it out, and a rollback is not gated', async ({ page }) => {
    await page.goto(edgeNginx)
    await expect(page.getByRole('checkbox', { name: 'Scan Image for Vulnerabilities Before Deploy' })).toBeChecked()

    await pickImageTag(page, VULNERABLE_TAG)
    await page.getByRole('button', { name: 'Review…' }).click()
    const review = page.getByRole('dialog')
    await expect(review.getByRole('heading', { name: 'Review changes to edge-nginx' })).toBeVisible()
    await review.getByRole('button', { name: /^Register and deploy :\d+$/ }).click()

    // The gate answers 409 and the panel follows the scan: waiting first, then the critical finding.
    const gate = review.getByRole('region', { name: 'Image scan' })
    await expect(gate).toBeVisible()
    await expect(gate.getByText('1 critical, 1 high, 1 medium')).toBeVisible({ timeout: 30_000 })
    await expect(review.getByRole('button', { name: 'Cancel' })).toBeVisible()

    await review.getByRole('button', { name: 'Deploy anyway' }).click()
    const started = page.getByText(/^Deploying edge-nginx to :\d+\.$/)
    await expect(started).toBeVisible()
    const revision = Number(/:(\d+)\.$/.exec((await started.textContent()) ?? '')?.[1])
    await expect(page.getByRole('region', { name: 'Rollout progress' }).getByText('Completed')).toBeVisible({ timeout: 40_000 })

    // Rolling back to an existing revision never goes through the gate.
    const earlier = revision - 1
    await page.goto(`${edgeNginx}?revision=${earlier}`)
    await page.getByRole('button', { name: 'Review…' }).click()
    const rollback = page.getByRole('dialog')
    await expect(rollback.getByRole('heading', { name: 'Review changes to edge-nginx' })).toBeVisible()
    await rollback.getByRole('button', { name: `Deploy :${earlier}` }).click()
    await expect(page.getByText(`Deploying edge-nginx to :${earlier}.`)).toBeVisible()
    await expect(page.getByRole('region', { name: 'Image scan' })).toHaveCount(0)
  })
})

test.describe('omer sees only what his tags allow', () => {
  test.use(asUser('omer'))

  test('/security lists the Staging image and hides the Prod one', async ({ page }) => {
    await page.goto('/security')
    await expect(page.getByRole('heading', { name: 'Image security' })).toBeVisible()
    await expect(page.getByRole('row', { name: /web/ })).toBeVisible()
    await expect(page.getByText('payments-api')).toHaveCount(0)
  })
})
