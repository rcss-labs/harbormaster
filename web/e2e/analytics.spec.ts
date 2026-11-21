import { asUser, expect, test } from './fixtures/test'

// Smoke test against the real backend: the sidebar leads to Analytics and every tab renders its first panel.
// The analytics endpoints land in the backend track, so this runs after both tracks are merged.
const TABS = [
  { link: 'Delivery', heading: 'Rollouts' },
  { link: 'Reliability', heading: 'Availability' },
  { link: 'Capacity', heading: 'Reserved vCPU, last 7 days' },
  { link: 'Operations', heading: 'Operations through Harbormaster' },
  { link: 'Cost', heading: 'Monthly spend' },
]

test.describe('analytics as sam', () => {
  test.use(asUser('sam'))

  test('sidebar leads to Analytics and the five tabs render', async ({ page }) => {
    await page.setViewportSize({ width: 1440, height: 900 })
    await page.goto('/')
    await page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: /Analytics/ }).click()
    await expect(page).toHaveURL(/\/analytics\/delivery/)
    await expect(page.getByRole('heading', { level: 1, name: 'Analytics' })).toBeVisible()

    for (const { link, heading } of TABS) {
      await page.getByRole('link', { name: new RegExp(`^${link}`) }).click()
      await expect(page).toHaveURL(new RegExp(`/analytics/${link.toLowerCase()}`))
      await expect(page.getByRole('heading', { name: heading, exact: true })).toBeVisible({ timeout: 15_000 })
      await expect(page.getByText('PROPOSED')).toHaveCount(0)
    }
  })

  // The fake estate starts with recent daily history, so the 90-day panels are ordinary panels with data.
  test('the 90-day panels render as normal panels with data', async ({ page }) => {
    await page.setViewportSize({ width: 1440, height: 900 })
    await page.goto('/analytics/reliability')
    const panel = page.getByRole('region', { name: 'Availability, last 90 days' })
    await expect(panel).toBeVisible({ timeout: 15_000 })
    await expect(panel).not.toHaveAttribute('data-variant', /.+/)
    await expect(panel.getByText('source service_availability_daily · kept')).toBeVisible()
    await expect(panel.getByRole('group', { name: /Availability/ })).toBeVisible()
    await expect(panel.getByText(/Daily data appears after the first rollup|Collecting daily data/)).toHaveCount(0)
    await expect(page.getByText('PROPOSED')).toHaveCount(0)

    await page.goto('/analytics/delivery')
    const reasons = page.getByRole('region', { name: 'Why rollouts failed' })
    await expect(reasons).toBeVisible({ timeout: 15_000 })
    await expect(reasons.getByText('No failed rollouts with a recorded reason in this range')).toHaveCount(0)
    await expect(page.getByText('PROPOSED')).toHaveCount(0)
  })

  // Read-only on purpose: the spec stops at the Review dialog so the shared estate is never changed.
  test('cost suggestions open the Deploy tab for review', async ({ page, context }) => {
    await page.setViewportSize({ width: 1440, height: 900 })
    await page.goto('/analytics/cost')
    const table = page.getByRole('table', { name: 'Rightsizing suggestions' })
    const jenkinsRow = table.getByRole('row', { name: /jenkins-controller/ })
    await expect(jenkinsRow).toBeVisible({ timeout: 15_000 })
    const suggestionAtDefault = await jenkinsRow.innerText()

    await page.getByLabel('Headroom above p95').fill('80')
    await expect(page.getByLabel('Headroom above p95')).toHaveValue('80')
    await expect.poll(async () => (await table.getByRole('row', { name: /jenkins-controller/ }).count()) === 0 || (await table.getByRole('row', { name: /jenkins-controller/ }).innerText()) !== suggestionAtDefault).toBe(true)
    await page.getByLabel('Headroom above p95').fill('30')
    await expect.poll(() => jenkinsRow.innerText()).toBe(suggestionAtDefault)

    const noSuggestion = page.getByRole('region', { name: 'No suggestion' })
    const certRenewer = noSuggestion.getByRole('listitem').filter({ hasText: 'cert-renewer' })
    await expect(certRenewer).toBeVisible()
    await expect(certRenewer).not.toContainText('$')

    await page.getByRole('region', { name: /^Rightsizing/ }).getByRole('button', { name: 'Draft change sets' }).click()
    const draft = page.getByRole('dialog')
    await expect(draft.getByText(/Nothing deploys yet\./)).toBeVisible()
    const [deployPage] = await Promise.all([
      context.waitForEvent('page'),
      draft.getByRole('listitem').filter({ hasText: 'jenkins-controller' }).getByRole('link', { name: 'Open in Deploy' }).click(),
    ])

    const pending = deployPage.getByRole('region', { name: 'Pending changes' })
    await expect(pending.getByRole('listitem', { name: 'Task CPU' })).toBeVisible({ timeout: 15_000 })
    await expect(pending.getByRole('listitem', { name: 'Task memory' })).toBeVisible()
    await pending.getByRole('button', { name: 'Review…' }).click()
    const review = deployPage.getByRole('dialog')
    await expect(review.getByRole('textbox', { name: 'Reason' })).toHaveValue('Rightsizing at 30% headroom')
    await review.getByRole('button', { name: 'Keep editing' }).click()
    await expect(review).toBeHidden()
    await deployPage.close()
  })

  test('ARM64 suggestions say which images block a move and fill the Deploy tab', async ({ page, context }) => {
    await page.setViewportSize({ width: 1440, height: 900 })
    await page.goto('/analytics/cost')
    const panel = page.getByRole('region', { name: /^ARM64 \(Graviton\)/ })
    const table = panel.getByRole('table', { name: 'ARM64 suggestions' })
    // The image platform check runs on the leader every few seconds; wait for it to read the seeded indexes.
    const payments = table.getByRole('row', { name: /payments-api/ })
    await expect(payments.getByText(/images? have an arm64 build/)).toContainText('of', { timeout: 30_000 })
    await expect(async () => {
      await page.reload()
      await expect(payments.getByRole('checkbox', { name: 'Select payments-api' })).toBeEnabled({ timeout: 2_000 })
    }).toPass({ timeout: 45_000 })
    const edge = table.getByRole('row', { name: /edge-nginx/ })
    await expect(edge.getByText('Blocked')).toBeVisible()
    await expect(edge.getByRole('checkbox', { name: 'Select edge-nginx' })).toBeDisabled()

    await panel.getByRole('button', { name: 'Draft change sets' }).click()
    const draft = page.getByRole('dialog')
    await expect(draft.getByText(/Nothing deploys yet\./)).toContainText('"Move to ARM64 (Graviton)"')
    const link = draft.getByRole('listitem').filter({ hasText: 'payments-api' }).getByRole('link', { name: 'Open in Deploy' })
    expect((await link.getAttribute('href')) ?? '').toContain('arch=ARM64')
    const [deployPage] = await Promise.all([context.waitForEvent('page'), link.click()])
    const pending = deployPage.getByRole('region', { name: 'Pending changes' })
    await expect(pending.getByRole('listitem', { name: 'CPU architecture' })).toContainText('ARM64', { timeout: 15_000 })
    await deployPage.close()
  })

  // Mutating: the apply changes catalog-api's capacity providers on the shared estate, which is why this runs in the flows project.
  test('Fargate Spot suggestions fill the Deploy tab and apply as one forced deployment', async ({ page, context }) => {
    await page.setViewportSize({ width: 1440, height: 900 })
    await page.goto('/analytics/cost')
    const panel = page.getByRole('region', { name: /^Fargate Spot/ })
    const table = panel.getByRole('table', { name: 'Fargate Spot candidates' })
    const catalogRow = table.getByRole('row', { name: /catalog-api/ })
    await expect(catalogRow).toBeVisible({ timeout: 15_000 })
    // The seeded terminal session two days ago makes it a candidate with a caution: shown, unticked.
    await expect(catalogRow.getByText('Check first')).toBeVisible()
    const box = catalogRow.getByRole('checkbox', { name: 'Select catalog-api' })
    await expect(box).not.toBeChecked()
    await expect(catalogRow.getByText('1 terminal sessions in 7 days')).toBeVisible()

    // edge-nginx would qualify, but its cluster was never associated with FARGATE_SPOT: said, with the saving it would have.
    const notCandidates = page.getByRole('region', { name: 'Not candidates' })
    const edge = notCandidates.getByRole('listitem').filter({ hasText: 'edge-nginx' })
    await expect(edge).toContainText('has no FARGATE_SPOT capacity provider')
    await expect(edge).toContainText('It would save about')
    await expect(notCandidates.getByRole('listitem').filter({ hasText: 'payments-api' })).toContainText('Production tag Prod')
    await expect(notCandidates.getByRole('listitem').filter({ hasText: 'notifications' })).toContainText('Already uses Fargate Spot')

    await box.check()
    await panel.getByRole('button', { name: 'Draft change sets' }).click()
    const draft = page.getByRole('dialog')
    await expect(draft.getByText(/Nothing deploys yet\./)).toContainText('"Move to Fargate Spot, 1 on-demand"')
    const link = draft.getByRole('listitem').filter({ hasText: 'catalog-api' }).getByRole('link', { name: 'Open in Deploy' })
    const href = (await link.getAttribute('href')) ?? ''
    expect(href).toContain('capacity=spot')
    const [deployPage] = await Promise.all([context.waitForEvent('page'), link.click()])

    const pending = deployPage.getByRole('region', { name: 'Pending changes' })
    await expect(pending.getByRole('listitem', { name: 'Capacity providers' })).toContainText('FARGATE (launch type)', { timeout: 15_000 })
    await expect(pending.getByRole('listitem', { name: 'Capacity providers' })).toContainText('FARGATE base 1, FARGATE_SPOT weight 1')
    await expect(deployPage.getByText(/Spot tasks can be stopped with two minutes' notice/)).toBeVisible()
    await pending.getByRole('button', { name: 'Review…' }).click()
    const review = deployPage.getByRole('dialog')
    await expect(review.getByRole('textbox', { name: 'Reason' })).toHaveValue('Move to Fargate Spot, 1 on-demand')
    await review.getByRole('button', { name: 'Apply changes' }).click()
    await expect(deployPage.getByText(/^(Deploying catalog-api to :\d+|Applied the changes to catalog-api)\.$/)).toBeVisible()

    const id = /\/services\/([^/]+)\/deploy/.exec(href)?.[1] ?? ''
    await expect
      .poll(async () => {
        const response = await deployPage.request.get(`/api/services/${id}/deploy-settings`)
        return ((await response.json()) as { capacity: { launch_type: string; strategy: { provider: string }[] } }).capacity
      })
      .toMatchObject({ launch_type: '', strategy: [{ provider: 'FARGATE' }, { provider: 'FARGATE_SPOT' }] })
    await deployPage.goto(`/services/${id}/operations`)
    await expect(deployPage.getByText('Changed capacity providers from FARGATE (launch type) to FARGATE base 1, FARGATE_SPOT weight 1.')).toBeVisible()
    await deployPage.close()
  })
})
