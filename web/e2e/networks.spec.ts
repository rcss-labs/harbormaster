import { expect, test, type Page } from '@playwright/test'
import { login, saveScreenshot, serviceId } from './helpers'

const HOMELAB_ACCOUNT_ID = '482109375512'
const LEGACY_VPC = 'vpc-5c1e0f2a'
// The network tier's first pass runs a few seconds after the server starts.
const POLL_TIMEOUT = 30_000

const TASK_CHIP = '[role="treeitem"] a[title^="Task "]'

/** The us-east-1 card inside the homelab-admin account section (other accounts have a us-east-1 card too). */
const homelabRegionCard = (page: Page) =>
  page.locator('section').filter({ has: page.getByRole('heading', { level: 2, name: /homelab-admin/ }) }).getByRole('link', { name: /us-east-1/ })

test('admin drills from accounts to the homelab-main tree', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 900 })
  await login(page, 'pavel')
  await page.goto('/networks')
  await expect(page.getByRole('heading', { level: 1, name: 'Networks' })).toBeVisible()

  const regionCard = homelabRegionCard(page)
  await expect(regionCard).toBeVisible({ timeout: POLL_TIMEOUT })
  await expect(regionCard).toContainText('14 tasks')
  await expect(regionCard).toContainText('3 clusters')
  await expect(regionCard).toContainText('2 active VPCs')
  await expect(regionCard).toContainText('1 VPC without tasks hidden')
  await regionCard.click()

  await page.getByRole('link', { name: /homelab-main/ }).click()
  await expect(page.getByRole('heading', { level: 1, name: 'homelab-main' })).toBeVisible()
  await expect(page.getByText('11 running tasks')).toBeVisible()

  const tree = page.getByRole('tree', { name: 'Network tree' })
  await expect(tree).toBeVisible()
  const chips = page.locator(TASK_CHIP)
  await expect(chips.first()).toBeVisible()
  await expect(chips).toHaveCount(11)
  // Admin sees Prod: the negative check for omer below only means something with this positive one.
  await expect(tree.getByText('core-services').first()).toBeVisible()
  await saveScreenshot(page, 'vpc-tree.png')

  await page.getByRole('button', { name: 'Collapse to services' }).click()
  await expect(chips).toHaveCount(0)
  await page.getByRole('button', { name: 'Expand all' }).click()
  await expect(chips).toHaveCount(11)

  // Keyboard: ArrowDown moves focus from the VPC item to the next visible item.
  const vpcItem = tree.getByRole('treeitem').first()
  await vpcItem.focus()
  await expect(vpcItem).toBeFocused()
  const focusedLabel = () => page.evaluate(() => document.activeElement?.textContent ?? '')
  const before = await focusedLabel()
  await page.keyboard.press('ArrowDown')
  await page.keyboard.press('ArrowDown')
  await expect.poll(focusedLabel).not.toBe(before)
  await expect(vpcItem).not.toBeFocused()
  await expect(tree.locator('[role="treeitem"]:focus')).toHaveCount(1)

  await tree.getByRole('link', { name: 'payments-api' }).first().click()
  await expect(page).toHaveURL(new RegExp(`/services/${serviceId(HOMELAB_ACCOUNT_ID, 'us-east-1', 'core-services', 'payments-api')}/overview$`))
})

test('dev user does not see Prod clusters in the tree', async ({ page }) => {
  await login(page, 'omer')
  await page.goto('/networks')
  await homelabRegionCard(page).click()
  await page.getByRole('link', { name: /homelab-main/ }).click()

  const tree = page.getByRole('tree', { name: 'Network tree' })
  await expect(tree).toBeVisible({ timeout: POLL_TIMEOUT })
  await expect(tree.getByText('background-jobs').first()).toBeVisible()
  await expect(tree.getByText('core-services')).toHaveCount(0)

  await page.goto(`/networks/${HOMELAB_ACCOUNT_ID}/us-east-1/${LEGACY_VPC}`)
  await expect(page.getByText('No running tasks you can see in this VPC.')).toBeVisible()
})

test('service networking card links to the tree', async ({ page }) => {
  await login(page, 'pavel')
  await page.goto(`/services/${serviceId(HOMELAB_ACCOUNT_ID, 'us-east-1', 'core-services', 'payments-api')}/overview`)
  await page.getByRole('link', { name: 'Network tree →' }).click()
  await expect(page).toHaveURL(new RegExp(`/networks/${HOMELAB_ACCOUNT_ID}/us-east-1/vpc-`))
  await expect(page.getByRole('heading', { level: 1, name: 'homelab-main' })).toBeVisible({ timeout: POLL_TIMEOUT })
  await expect(page.getByRole('tree', { name: 'Network tree' })).toBeVisible({ timeout: POLL_TIMEOUT })
})
