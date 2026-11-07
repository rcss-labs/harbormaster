import { expect, type Locator, type Page } from '@playwright/test'

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

/** A box in viewport coordinates, as `getBoundingClientRect` reports it. */
export interface Box {
  x: number
  y: number
  width: number
  height: number
}

/**
 * Reads the boxes of several elements in one `evaluate`, retrying until every element is attached and laid out.
 * `locator.boundingBox()` returns null when an element re-renders between the visibility check and the read, which
 * failed three gates; here a detached or stale element just makes the poll try again. The locators must share one page.
 */
export async function measureBoxes<T extends readonly Locator[]>(...locators: T): Promise<{ [K in keyof T]: Box }> {
  if (locators.length === 0) return [] as unknown as { [K in keyof T]: Box }
  const page = locators[0]!.page()
  let boxes: Box[] = []
  await expect
    .poll(
      async () => {
        try {
          const handles = await Promise.all(locators.map((locator) => locator.elementHandle({ timeout: 1000 })))
          const read = await page.evaluate((elements) => {
            const rects = elements.map((element) => {
              if (!element || !element.isConnected) return null
              const rect = element.getBoundingClientRect()
              return { x: rect.x, y: rect.y, width: rect.width, height: rect.height }
            })
            return rects.every((rect) => rect !== null) ? rects : null
          }, handles)
          if (read) boxes = read as Box[]
          return read !== null
        } catch {
          return false // the element went away mid-read: poll again
        }
      },
      { message: 'elements to be attached and measurable', timeout: 5000 },
    )
    .toBe(true)
  return boxes as unknown as { [K in keyof T]: Box }
}

/** The box of one element, read safely (see `measureBoxes`). */
export async function measureBox(locator: Locator): Promise<Box> {
  return (await measureBoxes(locator))[0]
}
