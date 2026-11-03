import { expect, type Page } from '@playwright/test'

export const SEED_PASSWORD = 'harbormaster-e2e-1'

/** Signs in through the login form and waits for the app shell. */
export async function login(page: Page, username: string, password = SEED_PASSWORD) {
  await page.goto('/login')
  // With single sign-on on, the password form is behind a link; without it the form is shown straight away.
  const passwordInstead = page.getByRole('button', { name: 'Sign in with a password instead' })
  const usernameField = page.getByLabel('Username')
  await passwordInstead.or(usernameField).first().waitFor()
  if (await passwordInstead.isVisible()) await passwordInstead.click()
  await usernameField.fill(username)
  await page.getByLabel('Password').fill(password)
  await page.getByRole('button', { name: 'Sign in' }).click()
  await expect(page.getByRole('navigation', { name: 'Main' })).toBeVisible()
}

/** Opaque service id: base64url (no padding) of account|region|cluster|service. */
export function serviceId(accountId: string, region: string, cluster: string, service: string) {
  return Buffer.from([accountId, region, cluster, service].join('|')).toString('base64url')
}

/** Picks a tag in the Deploy tab's image combobox of the named container; `search` narrows the list first (server-side `?q=`). */
export async function pickImageTag(page: Page, tag: string, options: { search?: string; container?: string } = {}) {
  if (options.container) await page.getByRole('group', { name: 'Container' }).getByRole('button', { name: options.container, exact: true }).click()
  const field = page.getByRole('combobox', { name: 'Image tag' })
  await field.click()
  if (options.search !== undefined) await field.fill(options.search)
  await page
    .getByRole('listbox', { name: 'Image tags' })
    .getByRole('option', { name: new RegExp(`^${tag.replace(/\./g, '\\.')}(?![\\w.-])`) })
    .click()
  await expect(field).toHaveValue(tag)
}

/** Opens the Deploy tab's review dialog, optionally types a reason, and presses the primary button (named `apply`). */
export async function reviewAndApply(page: Page, serviceName: string, apply: string | RegExp, reason?: string) {
  await page.getByRole('button', { name: 'Review…' }).click()
  const review = page.getByRole('dialog')
  await expect(review.getByRole('heading', { name: `Review changes to ${serviceName}` })).toBeVisible()
  if (reason !== undefined) await review.getByRole('textbox', { name: 'Reason' }).fill(reason)
  await review.getByRole('button', { name: apply }).click()
}

/** Saves a full-page screenshot into HM_E2E_SCREENSHOT_DIR when it is set (for visual review); a no-op otherwise. */
export async function saveScreenshot(page: Page, name: string) {
  const dir = process.env.HM_E2E_SCREENSHOT_DIR
  if (dir) await page.screenshot({ path: `${dir}/${name}`, fullPage: true })
}
