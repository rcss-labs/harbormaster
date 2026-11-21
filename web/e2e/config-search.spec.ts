import fs from 'node:fs/promises'
import type { Page } from '@playwright/test'
import { asUser, expect, test } from './fixtures/test'

// Config search against the demo estate. Read-only (nothing here changes the server), so it runs with the observe specs on
// the unmutated seed. sam is a global admin; omer sees Dev and Staging only.

async function search(page: Page, query: string) {
  await page.goto('/config')
  await page.getByRole('textbox', { name: 'Config search query' }).fill(query)
  await page.getByRole('form', { name: 'Config search' }).getByRole('button', { name: 'Search', exact: true }).click()
  await expect(page.getByRole('table', { name: 'Config search results' })).toBeVisible()
}

const resultNames = (page: Page) => page.getByRole('table', { name: 'Config search results' }).locator('tbody td:nth-child(2) a')

test.describe('as sam', () => {
  test.use(asUser('sam'))

  test('finds services by environment name, port and secret', async ({ page }) => {
    await search(page, 'env:NODE_ENV')
    await expect(resultNames(page)).toHaveText(['api', 'payments-api'])
    await search(page, 'port:443')
    await expect(resultNames(page)).toHaveText(['edge-nginx'])
    await search(page, 'secret:DATABASE_URL')
    await expect(resultNames(page)).toHaveText(['api', 'auth-service'])
  })

  test('finds services by base image', async ({ page }) => {
    await search(page, 'base:node:18')
    await expect(resultNames(page)).toHaveText(['web', 'web-frontend'])
  })

  // The fake estate finishes notifications' rollout a few steps after startup, so whether the old :9 still has tasks when
  // this runs is not fixed. The previous-revision label is covered by TestConfigSearch_PreviousRevisionIsLabelled and the
  // page tests. Here the new tag must find the service either way, and as the PRIMARY revision it is never labelled.
  test('finds a service in a rollout by its new tag without a previous-revision label', async ({ page }) => {
    await search(page, 'tag:2.3.0')
    const row = page.getByRole('row').filter({ hasText: 'notifications' })
    await expect(row).toBeVisible()
    await expect(row).not.toContainText('Previous revision')
  })

  test('opens the task definition from a result', async ({ page }) => {
    await search(page, 'secret:JWT_SIGNING_KEY')
    await resultNames(page).filter({ hasText: 'auth-service' }).click()
    await expect(page).toHaveURL(/\/services\/[^/]+\/task-definition$/)
  })

  test('exports CSV with names and no values', async ({ page }) => {
    await search(page, 'secret:DATABASE_URL')
    const [download] = await Promise.all([page.waitForEvent('download'), page.getByRole('button', { name: 'Export CSV' }).click()])
    expect(download.suggestedFilename()).toMatch(/^harbormaster-config-search-\d{8}-\d{4}\.csv$/)
    const text = await fs.readFile((await download.path()) as string, 'utf8')
    const lines = text.split('\r\n').filter((line) => line !== '')
    const header = lines[0].split(',')
    expect(header).toContain('env_names')
    const apiLine = lines.find((line) => line.startsWith('api,'))
    expect(apiLine, 'a line for api').toBeDefined()
    // The staging api sets NODE_ENV=staging: only the name is exported, in the env_names cell.
    expect((apiLine as string).split(',')[header.indexOf('env_names')]).toBe('NODE_ENV')
    expect((apiLine as string).split(',')[header.indexOf('secret_names')]).toBe('DATABASE_URL')
    // Values and secret references never reach the file.
    for (const value of ['secretsmanager', 'auth.rcss.local', 'ops@rcss.local']) expect(text).not.toContain(value)
  })
})

test.describe('as omer', () => {
  test.use(asUser('omer'))

  test('a restricted user sees only their services', async ({ page, browser }) => {
    await search(page, 'secret:DATABASE_URL')
    await expect(resultNames(page)).toHaveText(['api'])
    await expect(resultNames(page).filter({ hasText: 'auth-service' })).toHaveCount(0)
    const omerSearched = Number((await page.getByText(/^Searched \d+ services you can see\.$/).innerText()).match(/\d+/)?.[0])

    const samContext = await browser.newContext(asUser('sam'))
    const samPage = await samContext.newPage()
    await search(samPage, 'secret:DATABASE_URL')
    const samSearched = Number((await samPage.getByText(/^Searched \d+ services you can see\.$/).innerText()).match(/\d+/)?.[0])
    await samContext.close()
    expect(omerSearched).toBeGreaterThan(0)
    expect(omerSearched).toBeLessThan(samSearched)
  })
})
