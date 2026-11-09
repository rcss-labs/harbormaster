import { DAY_NAMES, awakeHourUtc, clearScheduleSlot, createSchedule, desiredTasks, restoreSchedules, type SavedSchedule, startSchedule } from './offhours.helpers'
import { serviceUrl } from './fixtures/ids'
import { asUser, expect, test } from './fixtures/test'
import { serviceId } from './helpers'

// Off-hours schedules end to end. The fake AWS does not run cron or at actions; the reconciler enforces the state
// every HM_OFFHOURS_INTERVAL (2s in playwright.config.ts). Nothing here depends on the wall clock: the only awake hour
// is twelve hours away, so the present hour is asleep whenever the run starts, and the schedule is deleted at the end
// (also on failure) so the estate is back to how it was seeded.
test.describe.configure({ mode: 'serial' })

const NAME = 'e2e nights'
const STAGING_ACCOUNT = '731022849906'
const stagingWebId = serviceId(STAGING_ACCOUNT, 'us-east-1', 'staging-web', 'web')
const stagingWebUrl = serviceUrl(STAGING_ACCOUNT, 'us-east-1', 'staging-web', 'web')

test.describe('sam', () => {
  test.use(asUser('sam'))

  let aside: SavedSchedule[] = []
  let seededTasks = 0
  test.beforeEach(async ({ request }) => {
    aside = []
    await clearScheduleSlot(request, NAME, 'env=staging', aside)
    // Read after the slot is cleared, and require tasks, so a leak from an earlier run (a service left asleep) cannot hide.
    seededTasks = await desiredTasks(request, stagingWebId)
    expect(seededTasks).toBeGreaterThan(0)
  })
  test.afterEach(async ({ request }) => {
    await restoreSchedules(request, NAME, aside)
    // Deleting a schedule only kicks the reconciler; wait for the capacity to be back before the next test.
    await expect.poll(() => desiredTasks(request, stagingWebId), { timeout: 15_000 }).toBe(seededTasks)
  })

  test('a staging schedule puts web to sleep, wakes it for two hours, sleeps it again and restores it on delete', async ({ page, request }) => {
    const stagingCount = (await (await request.get('/api/offhours/tags')).json()).items.find(
      (option: { key: string; value: string }) => option.key === 'env' && option.value === 'staging',
    ).services as number

    await startSchedule(page, NAME, 'env=staging')
    for (const day of DAY_NAMES) await page.getByRole('button', { name: `${day} ${awakeHourUtc()}:00 asleep` }).click()
    await expect(page.getByText(`${stagingCount} services will go to sleep when you save.`)).toBeVisible()

    await page.getByRole('button', { name: 'Save', exact: true }).click()
    await expect(page.getByText('Saved.')).toBeVisible()

    const webRow = page
      .getByRole('table', { name: 'Services on this schedule' })
      .getByRole('row')
      .filter({ has: page.locator('strong', { hasText: /^web$/ }) })
    await expect(webRow).toContainText('Asleep until', { timeout: 15_000 })
    await expect(webRow).toContainText(`${seededTasks} → 0`)

    await webRow.getByRole('button', { name: 'Wake for 2 hours' }).click()
    await expect(webRow).toContainText('Woken until', { timeout: 15_000 })
    await webRow.getByRole('button', { name: 'Put back to sleep' }).click()
    await expect(webRow).toContainText(/Asleep/, { timeout: 15_000 })
    await expect(webRow).not.toContainText('Woken')

    await page.getByRole('button', { name: 'Delete', exact: true }).click()
    await page.getByRole('dialog').getByRole('button', { name: 'Delete schedule' }).click()
    await expect(page.getByText('Schedule deleted.')).toBeVisible()

    await expect.poll(() => desiredTasks(request, stagingWebId), { timeout: 15_000 }).toBe(seededTasks)
    await page.goto(stagingWebUrl)
    await expect(page.getByRole('heading', { name: 'web', exact: true }).first()).toBeVisible()
    await expect(page.getByRole('heading', { name: 'Off-hours', exact: true })).toHaveCount(0)
  })

  test('a schedule on env=prod shows the production warning, and discarding saves nothing', async ({ page, request }) => {
    await startSchedule(page, NAME, 'env=prod')
    await expect(page.getByText(/\d+ matched services? looks? like production:/)).toBeVisible()
    await page.getByRole('button', { name: 'Discard' }).click()
    const list = await (await request.get('/api/offhours/schedules')).json()
    expect((list.items as { name: string }[]).filter((item) => item.name === NAME)).toEqual([])
  })
})

test.describe('oncall (Operate on Staging)', () => {
  test.use(asUser('oncall'))

  test('wakes a sleeping staging service from its Overview card', async ({ page, browser }) => {
    // sam's session creates the schedule through the API; oncall only has Operate on the service.
    const admin = await browser.newContext({ ...asUser('sam'), baseURL: test.info().project.use.baseURL })
    const aside: SavedSchedule[] = []
    let seededTasks = 0
    try {
      await clearScheduleSlot(admin.request, NAME, 'env=staging', aside)
      seededTasks = await desiredTasks(admin.request, stagingWebId)
      expect(seededTasks).toBeGreaterThan(0)
      await createSchedule(admin.request, NAME, 'env=staging')
      await page.goto(stagingWebUrl)
      const card = page.locator('section', { has: page.getByRole('heading', { name: 'Off-hours' }) })
      await expect(card).toContainText('Asleep', { timeout: 15_000 })
      await card.getByRole('button', { name: 'Wake for 2 hours' }).click()
      await expect(card).toContainText('Woken until', { timeout: 15_000 })
      await expect(card.getByRole('button', { name: 'Put back to sleep' })).toBeVisible()
    } finally {
      try {
        await restoreSchedules(admin.request, NAME, aside)
        if (seededTasks > 0) await expect.poll(() => desiredTasks(admin.request, stagingWebId), { timeout: 15_000 }).toBe(seededTasks)
      } finally {
        await admin.close()
      }
    }
  })
})

