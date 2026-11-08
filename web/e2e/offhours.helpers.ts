import { expect, type APIRequestContext, type Page } from '@playwright/test'

export const DAY_NAMES = ['Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat', 'Sun'] as const

/** The one hour awake in the e2e schedules: twelve hours from now in UTC, so the current hour is asleep whatever time the run starts. */
export const awakeHourUtc = () => (new Date().getUTCHours() + 12) % 24

/** A 7 x 24 awake grid with only `hour` awake, on every day. */
export const gridAwakeOnlyAt = (hour: number) => DAY_NAMES.map(() => Array.from({ length: 24 }, (_, h) => h === hour))

/** The CSRF header every API write needs, read from the signed-in session. */
async function writeHeaders(request: APIRequestContext) {
  const me = (await (await request.get('/api/me')).json()) as { csrf_token: string }
  return { 'X-CSRF-Token': me.csrf_token }
}

interface SavedSchedule {
  name: string
  enabled: boolean
  tag_key: string
  tag_value: string
  timezone: string
  awake: boolean[][]
}

/**
 * Takes aside the schedules that would collide with an e2e one (a tag allows one schedule, and the demo estate seeds
 * "Staging nights and weekends" on env=staging, disabled): deletes the e2e schedule by name and any other schedule on
 * the tag, and returns what was removed so `restoreSchedules` can put the estate back.
 */
export async function clearScheduleSlot(request: APIRequestContext, name: string, tag: string): Promise<SavedSchedule[]> {
  const [tagKey, tagValue] = tag.split('=')
  const headers = await writeHeaders(request)
  const list = await (await request.get('/api/offhours/schedules')).json()
  const aside: SavedSchedule[] = []
  for (const item of (list.items ?? []) as { id: number; name: string; tag_key: string; tag_value: string }[]) {
    const onTag = item.tag_key === tagKey && item.tag_value === tagValue
    if (item.name !== name && !onTag) continue
    if (item.name !== name) {
      const d = await (await request.get(`/api/offhours/schedules/${item.id}`)).json()
      aside.push({ name: d.name, enabled: d.enabled, tag_key: d.tag_key, tag_value: d.tag_value, timezone: d.timezone, awake: d.awake })
    }
    await request.delete(`/api/offhours/schedules/${item.id}`, { headers })
  }
  return aside
}

/** Removes the e2e schedule and recreates the schedules `clearScheduleSlot` set aside. */
export async function restoreSchedules(request: APIRequestContext, name: string, aside: SavedSchedule[]) {
  const headers = await writeHeaders(request)
  const list = await (await request.get('/api/offhours/schedules')).json()
  for (const item of (list.items ?? []) as { id: number; name: string }[]) {
    if (item.name === name) await request.delete(`/api/offhours/schedules/${item.id}`, { headers })
  }
  for (const schedule of aside) {
    const response = await request.post('/api/offhours/schedules', { data: schedule, headers })
    expect(response.ok(), await response.text()).toBe(true)
  }
}

/** Creates a UTC schedule over the tag through the API and returns its id. */
export async function createSchedule(request: APIRequestContext, name: string, tag: string) {
  const [tagKey, tagValue] = tag.split('=')
  const response = await request.post('/api/offhours/schedules', {
    headers: await writeHeaders(request),
    data: { name, enabled: true, tag_key: tagKey, tag_value: tagValue, timezone: 'UTC', awake: gridAwakeOnlyAt(awakeHourUtc()) },
  })
  expect(response.ok(), await response.text()).toBe(true)
  return (await response.json()).id as number
}

/** Tasks the service wants right now, from the service API. */
export async function desiredTasks(request: APIRequestContext, id: string) {
  return (await (await request.get(`/api/services/${id}`)).json()).desired as number
}

/** Opens the new-schedule editor and fills name, tag and UTC. Leaves the grid empty. */
export async function startSchedule(page: Page, name: string, tag: string) {
  await page.goto('/settings/offhours')
  await page.getByRole('button', { name: 'Add schedule' }).first().click()
  await page.getByLabel('Name', { exact: true }).fill(name)
  await page.getByLabel('Services tagged').fill(tag)
  await page.getByLabel('Timezone').selectOption('UTC')
}
