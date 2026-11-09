import { clearScheduleSlot, createSchedule, desiredTasks, restoreSchedules, type SavedSchedule } from './offhours.helpers'
import { serviceUrl } from './fixtures/ids'
import { watchPage } from './fixtures/phone'
import { asUser, expect, test } from './fixtures/test'
import { serviceId } from './helpers'

// Phone flows for off-hours. They run after the flow specs, create their own schedule and delete it again.
test.use({ ...asUser('sam'), viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true, deviceScaleFactor: 3 })

const NAME = 'e2e phone nights'
const stagingWebId = serviceId('731022849906', 'us-east-1', 'staging-web', 'web')

test('the page does not scroll sideways, the grid scrolls inside its panel and a wake from the card works', async ({ page, request }) => {
  watchPage(page)
  const aside: SavedSchedule[] = []
  let seededTasks = 0
  try {
    await clearScheduleSlot(request, NAME, 'env=staging', aside)
    seededTasks = await desiredTasks(request, stagingWebId)
    expect(seededTasks).toBeGreaterThan(0)
    await createSchedule(request, NAME, 'env=staging')
    await page.goto('/settings/offhours')
    await page.getByRole('list', { name: 'Schedules' }).getByRole('button', { name: new RegExp(NAME) }).click()
    await expect(page.getByLabel('Name', { exact: true })).toHaveValue(NAME)

    const widths = await page.evaluate(() => {
      const grid = document.querySelector('[aria-label="Awake hours"]')
      const scroller = grid?.parentElement
      return {
        pageScroll: document.documentElement.scrollWidth,
        pageClient: document.documentElement.clientWidth,
        gridScroll: scroller?.scrollWidth ?? 0,
        gridClient: scroller?.clientWidth ?? 0,
        overflowX: scroller ? getComputedStyle(scroller).overflowX : '',
      }
    })
    expect(widths.pageScroll).toBeLessThanOrEqual(widths.pageClient)
    expect(widths.gridScroll).toBeGreaterThan(widths.gridClient)
    expect(['auto', 'scroll']).toContain(widths.overflowX)

    await page.goto(serviceUrl('731022849906', 'us-east-1', 'staging-web', 'web'))
    const card = page.locator('section', { has: page.getByRole('heading', { name: 'Off-hours' }) })
    await expect(card).toContainText('Asleep', { timeout: 15_000 })
    await card.getByRole('button', { name: 'Wake for 2 hours' }).click()
    await expect(card).toContainText('Woken until', { timeout: 15_000 })
  } finally {
    await restoreSchedules(request, NAME, aside)
    if (seededTasks > 0) await expect.poll(() => desiredTasks(request, stagingWebId), { timeout: 15_000 }).toBe(seededTasks)
  }
})
