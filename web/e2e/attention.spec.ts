import { asUser, expect, test } from './fixtures/test'
import { SVC, serviceUrl } from './fixtures/ids'

// Mutates image-resizer (a restart), so it runs in chromium-flows.
//
// Which alerts name a change depends on timing the fake only gives for a failing rollout: a failure alert is stamped when
// a task stops, a moment after the deploy that started it. The only failing rollout the fake produces is an OOM revision
// of image-resizer, which change.spec registers and counts revisions of, so the positive case (a failure alert naming its
// deploy) is covered by the Go test TestAlerts_JustBeforeFromOperationLog and not repeated here. What stays checkable
// without a race is the negative side: an alert with no change before it, and a rollout alert after a change, name nothing.
test.use(asUser('sam'))

const imageResizer = (tab?: string) => serviceUrl(...SVC.imageResizer, tab)

interface AlertItem {
  kind: string
  service: string
  just_before: { text: string; source: string } | null
}

async function listAlerts(request: { get(url: string): Promise<{ json(): Promise<unknown> }> }): Promise<AlertItem[]> {
  return ((await (await request.get('/api/alerts')).json()) as { items: AlertItem[] }).items
}

test('an alert with no change before it, and a rollout alert after a restart, show no Just before line', async ({ page }) => {
  // Nothing has changed image-resizer yet (no earlier flow spec touches it): its alerts name no change.
  await page.goto('/')
  const attention = page.getByRole('region', { name: 'Needs attention' })
  const resizerRows = attention.getByTestId('alert-row').filter({ hasText: SVC.imageResizer[3] })
  await expect(resizerRows.first()).toBeVisible()
  await expect(resizerRows.first()).not.toContainText('Just before')
  const before = (await listAlerts(page.request)).filter((alert) => alert.service === 'image-resizer')
  expect(before.length).toBeGreaterThan(0)
  for (const alert of before) expect(alert.just_before, `${alert.kind} before any change`).toBeNull()

  // A Harbormaster change now exists. A rollout alert is stamped when ECS creates the deployment, before the change is
  // recorded, so whichever rollout alerts exist must not name it (or the previous change).
  await page.goto(imageResizer())
  const restart = page.getByRole('button', { name: 'Restart (new deployment)' })
  await expect(restart).toBeEnabled()
  await restart.click()
  await page.getByRole('dialog').getByRole('button', { name: 'Restart', exact: true }).click()
  await expect(page.getByText('Restart started.')).toBeVisible()

  for (const alert of (await listAlerts(page.request)).filter((item) => item.kind === 'rollout_in_progress')) {
    expect(alert.just_before, `${alert.service} rollout alert`).toBeNull()
  }

  // Leave no open rollout behind: change.spec expects Restart to be enabled.
  await page.goto(imageResizer())
  await expect(page.getByRole('button', { name: 'Restart (new deployment)' })).toBeEnabled({ timeout: 40_000 })
})
