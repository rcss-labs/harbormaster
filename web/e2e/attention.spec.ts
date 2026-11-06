import type { Page } from '@playwright/test'
import { asUser, expect, test } from './fixtures/test'
import { SVC, serviceUrl } from './fixtures/ids'
import { pickImageTag, reviewAndApply, serviceId } from './helpers'

// Deploys, so it runs in chromium-flows. The fake backend deterministically follows a web-frontend deploy with an
// info alert ("rollout_in_progress", stamped when ECS creates the deployment), so the first branch of the plan applies:
// that later alert row carries "Just before: Deployed …". It lives only while the rollout runs (a few seconds) and the
// poller can miss it, so the test watches the API for it and retries the deploy a couple of times.
test.use(asUser('pavel'))

const webFrontend = ['482109375512', 'us-east-1', 'core-services', 'web-frontend'] as const

async function otherImageTag(page: Page): Promise<string> {
  const id = serviceId(...webFrontend)
  const response = await page.request.get(`/api/services/${id}/image-tags`)
  expect(response.ok()).toBe(true)
  const { current, tags } = (await response.json()) as { current: string; tags: { tag: string }[] }
  const other = tags.map((entry) => entry.tag).find((tag) => tag !== current)
  expect(other, 'web-frontend needs a second image tag to deploy').toBeDefined()
  return other as string
}

/** Deploys the other image tag through the Deploy tab and returns the revision it registered. */
async function deployOtherTag(page: Page): Promise<number> {
  const tag = await otherImageTag(page)
  await page.goto(serviceUrl(...webFrontend, 'deploy'))
  // A deploy during a rollout is refused as a stale edit, so wait until the page itself no longer shows one.
  await expect(page.getByRole('main').getByText('Deploying', { exact: true })).toHaveCount(0, { timeout: 30_000 })
  await pickImageTag(page, tag)
  await reviewAndApply(page, 'web-frontend', /^Register and deploy :\d+$/)
  const toast = page.getByText(/^Deploying web-frontend to :\d+\.$/)
  await expect(toast).toBeVisible()
  return Number(/:(\d+)\.$/.exec((await toast.textContent()) ?? '')?.[1])
}

/** Waits until the service list reports `revision` as deployed with its rollout done (the list lags a deploy by one poll). */
async function waitForRolloutDone(page: Page, revision: number) {
  await expect
    .poll(
      async () => {
        const services = (await (await page.request.get('/api/services')).json()) as { items: { name: string; revision: number; rollout: string | null }[] }
        const service = services.items.find((item) => item.name === 'web-frontend')
        return service?.revision === revision && service.rollout !== 'in_progress'
      },
      { timeout: 40_000 },
    )
    .toBe(true)
}

/** Deploys again and reports whether the rollout alert showed up in /api/alerts while the rollout ran. */
async function deployAndWatchForAlert(page: Page): Promise<{ seen: boolean; revision: number }> {
  const revision = await deployOtherTag(page)
  // The alert lives only while ECS reports the rollout (two fake-ECS steps for web-frontend's two tasks), which is about
  // one poll interval, so the poller can miss it. Catch it through the API (cheap, 100 ms apart) and open the Overview
  // straight away; a miss is retried with a fresh deploy by the caller.
  const deadline = Date.now() + 15_000
  while (Date.now() < deadline) {
    const alerts = (await (await page.request.get('/api/alerts')).json()) as { items: { title: string }[] }
    if (alerts.items.some((alert) => alert.title.startsWith('web-frontend'))) return { seen: true, revision }
    await page.waitForTimeout(100)
  }
  return { seen: false, revision }
}

// The rollout alert is stamped when ECS creates its deployment, a moment before Harbormaster records the deploy, so the
// first deploy of a service never counts as "just before" its own alert. A second deploy does: its alert follows the first.
test('an alert that follows a deploy of web-frontend shows what was deployed just before', async ({ page }) => {
  test.setTimeout(120_000)
  await waitForRolloutDone(page, await deployOtherTag(page))

  let alertSeen = false
  let lastRevision = 0
  for (let attempt = 0; attempt < 3 && !alertSeen; attempt++) {
    const { seen, revision } = await deployAndWatchForAlert(page)
    alertSeen = seen
    lastRevision = revision
    if (!seen) await waitForRolloutDone(page, revision) // a missed rollout must finish before the next deploy
  }
  expect(alertSeen, 'no alert for web-frontend appeared in three deploys').toBe(true)
  await page.goto('/')
  const row = page.getByRole('region', { name: 'Needs attention' }).getByTestId('alert-row').filter({ hasText: 'web-frontend' })
  await expect(row.first()).toContainText(/Just before: Deployed/)

  // The seeded image-resizer failure predates any change in the log, so its row stays free of a Just before line
  // (this test runs before change.spec touches image-resizer).
  const resizer = page.getByRole('region', { name: 'Needs attention' }).getByTestId('alert-row').filter({ hasText: SVC.imageResizer[3] })
  await expect(resizer.first()).toBeVisible()
  await expect(resizer.first()).not.toContainText('Just before')

  // Leave no rollout running for the next spec (or the next repeat).
  await waitForRolloutDone(page, lastRevision)
})
