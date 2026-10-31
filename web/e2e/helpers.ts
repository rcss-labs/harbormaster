import { expect, type Page } from '@playwright/test'

export const SEED_PASSWORD = 'harbormaster-e2e-1'

/** Signs in through the login form and waits for the app shell. */
export async function login(page: Page, username: string, password = SEED_PASSWORD) {
  await page.goto('/login')
  await page.getByLabel('Username').fill(username)
  await page.getByLabel('Password').fill(password)
  await page.getByRole('button', { name: 'Sign in' }).click()
  await expect(page.getByRole('navigation', { name: 'Main' })).toBeVisible()
}

/** Opaque service id: base64url (no padding) of account|region|cluster|service. */
export function serviceId(accountId: string, region: string, cluster: string, service: string) {
  return Buffer.from([accountId, region, cluster, service].join('|')).toString('base64url')
}
