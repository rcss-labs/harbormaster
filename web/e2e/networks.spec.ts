import { asUser, expect, test, type Page } from './fixtures/test'
import { login, saveScreenshot, serviceId } from './helpers'

const HOMELAB_ACCOUNT_ID = '482109375512'
const LEGACY_VPC = 'vpc-5c1e0f2a'
const MAIN_VPC = 'vpc-0a41c7e29b3d'
// The network tier's first pass runs a few seconds after the server starts.
const POLL_TIMEOUT = 30_000

const vpcList = (page: Page) => page.getByRole('region', { name: 'VPCs' })
const placement = (page: Page) => page.getByRole('region', { name: 'Placement' })
const vpcUrl = (vpc: string) => `/networks?vpc=${encodeURIComponent(`${HOMELAB_ACCOUNT_ID}/us-east-1/${vpc}`)}`

test('admin picks homelab-main from the VPC list and walks the placement outline', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 900 })
  await login(page, 'sam')
  await page.goto('/networks')
  await expect(page.getByRole('heading', { level: 1, name: 'Networks' })).toBeVisible()

  const main = vpcList(page).getByRole('button', { name: /homelab-main/ })
  await expect(main).toBeVisible({ timeout: POLL_TIMEOUT })
  await expect(main).toContainText('2 cl')
  await expect(vpcList(page).getByText('1 VPC without tasks hidden')).toBeVisible()
  await main.click()
  await expect(page).toHaveURL(new RegExp(`vpc=${HOMELAB_ACCOUNT_ID}%2Fus-east-1%2F${MAIN_VPC}`))
  await expect(page.getByRole('heading', { level: 2, name: 'homelab-main' })).toBeVisible()
  await expect(page.getByRole('list', { name: 'VPC summary' })).toContainText('11')

  // Clusters are collapsed to start with; an admin sees Prod, which the omer test below must not.
  const core = placement(page).getByRole('button', { name: /core-services/ }).first()
  await expect(core).toHaveAttribute('aria-expanded', 'false')
  await expect(placement(page).getByRole('link', { name: 'payments-api' })).toHaveCount(0)
  await saveScreenshot(page, 'vpc-placement.png')

  await placement(page).getByRole('button', { name: 'Expand all' }).click()
  await expect(placement(page).getByRole('link', { name: 'payments-api' }).first()).toBeVisible()
  await placement(page).getByRole('button', { name: 'Collapse to clusters' }).click()
  await expect(placement(page).getByRole('link', { name: 'payments-api' })).toHaveCount(0)

  await core.click()
  await placement(page).getByRole('link', { name: 'payments-api' }).first().click()
  await expect(page).toHaveURL(new RegExp(`/services/${serviceId(HOMELAB_ACCOUNT_ID, 'us-east-1', 'core-services', 'payments-api')}/overview$`))
})

test('dev user does not see Prod clusters in the placement outline', async ({ page }) => {
  await login(page, 'omer')
  await page.goto(vpcUrl(MAIN_VPC))

  await expect(placement(page)).toBeVisible({ timeout: POLL_TIMEOUT })
  await expect(placement(page).getByRole('button', { name: /background-jobs/ }).first()).toBeVisible()
  await expect(placement(page).getByRole('button', { name: /core-services/ })).toHaveCount(0)

  await page.goto(vpcUrl(LEGACY_VPC))
  await expect(page.getByText('No running tasks you can see in this VPC.')).toBeVisible({ timeout: POLL_TIMEOUT })
})

test('service networking card opens the VPC on the Networks page', async ({ page }) => {
  await login(page, 'sam')
  await page.goto(`/services/${serviceId(HOMELAB_ACCOUNT_ID, 'us-east-1', 'core-services', 'payments-api')}/overview`)
  await page.getByRole('link', { name: 'Network placement →' }).click()
  // The old /networks/<account>/<region>/<vpc> link redirects to the selection in the URL.
  await expect(page).toHaveURL(new RegExp(`/networks\\?vpc=${HOMELAB_ACCOUNT_ID}%2Fus-east-1%2Fvpc-`))
  await expect(page.getByRole('heading', { level: 2, name: 'homelab-main' })).toBeVisible({ timeout: POLL_TIMEOUT })
  await expect(placement(page)).toBeVisible({ timeout: POLL_TIMEOUT })
})

test.describe('networks as sam', () => {
  test.use(asUser('sam'))

  test('lists the VPCs of both accounts, grouped by account', async ({ page }) => {
    await page.goto('/networks')
    await expect(vpcList(page).getByRole('button', { name: /homelab-main/ })).toBeVisible({ timeout: POLL_TIMEOUT })
    await expect(vpcList(page).getByRole('button', { name: /shared-services/ })).toBeVisible()
    await expect(vpcList(page).getByText('homelab-admin', { exact: true })).toBeVisible()
    await expect(vpcList(page).getByText('staging-example', { exact: true })).toBeVisible()
    await expect(vpcList(page).getByRole('button', { name: /legacy-default/ })).toHaveCount(0)
  })

  test('the filter narrows the list and the old region URL still works', async ({ page }) => {
    await page.goto('/networks')
    await expect(vpcList(page).getByRole('button', { name: /homelab-main/ })).toBeVisible({ timeout: POLL_TIMEOUT })
    await page.getByRole('searchbox', { name: 'Filter VPCs' }).fill('staging')
    await expect(vpcList(page).getByRole('button', { name: /homelab-main/ })).toHaveCount(0)
    await expect(page).toHaveURL(/q=staging/)

    await page.goto(`/networks/${HOMELAB_ACCOUNT_ID}/us-east-1`)
    await expect(page).toHaveURL(/\/networks\?q=us-east-1/)
    await expect(vpcList(page).getByRole('button', { name: /homelab-main/ })).toBeVisible({ timeout: POLL_TIMEOUT })
  })

  test('homelab-main shows subnets, the placement zones and the NAT gateway', async ({ page }) => {
    await page.goto(vpcUrl(MAIN_VPC))
    await expect(page.getByRole('heading', { level: 2, name: 'homelab-main' })).toBeVisible({ timeout: POLL_TIMEOUT })
    await expect(page.getByRole('region', { name: 'Gateways and endpoints' }).getByText(/nat-/)).toBeVisible()

    await expect(page.getByRole('table', { name: 'Subnets' }).getByText('subnet-03f1a9c2')).toBeVisible()
    await expect(placement(page).getByText('us-east-1a')).toBeVisible()
    await expect(placement(page).getByRole('button', { name: /core-services/ }).first()).toBeVisible()
  })
})
