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

export interface ScheduleTag {
  kind: 'aws' | 'harbormaster'
  key?: string
  value?: string
  tag_id?: number
}

export interface SavedSchedule {
  name: string
  enabled: boolean
  tags: ScheduleTag[]
  excluded_service_ids: string[]
  timezone: string
  awake: boolean[][]
}

async function deleteSchedule(request: APIRequestContext, id: number, headers: Record<string, string>) {
  const response = await request.delete(`/api/offhours/schedules/${id}`, { headers })
  expect(response.ok(), await response.text()).toBe(true)
}

/**
 * Takes aside the schedules that would collide with an e2e one (a service belongs to one schedule, and the demo estate
 * seeds "Staging nights and weekends" on env=staging, disabled): deletes the e2e schedule by name and any other
 * schedule that selects the AWS tag. What it removed is pushed onto `aside` as it goes, so a caller whose `finally`
 * runs `restoreSchedules` gets back everything collected even if a later request throws.
 */
export async function clearScheduleSlot(request: APIRequestContext, name: string, tag: string, aside: SavedSchedule[]) {
  const [tagKey, tagValue] = tag.split('=')
  const headers = await writeHeaders(request)
  const list = await (await request.get('/api/offhours/schedules')).json()
  for (const item of (list.items ?? []) as { id: number; name: string; tags: ScheduleTag[] }[]) {
    const onTag = item.tags.some((t) => t.kind === 'aws' && t.key === tagKey && t.value === tagValue)
    if (item.name !== name && !onTag) continue
    if (item.name !== name) {
      const d = await (await request.get(`/api/offhours/schedules/${item.id}`)).json()
      aside.push({
        name: d.name,
        enabled: d.enabled,
        tags: d.tags.map((t: { kind: 'aws' | 'harbormaster'; key: string; value: string; tag_id: number }) =>
          t.kind === 'aws' ? { kind: 'aws', key: t.key, value: t.value } : { kind: 'harbormaster', tag_id: t.tag_id },
        ),
        excluded_service_ids: d.excluded.map((e: { service_id: string }) => e.service_id),
        timezone: d.timezone,
        awake: d.awake,
      })
    }
    await deleteSchedule(request, item.id, headers)
  }
}

/** Removes the e2e schedule and recreates the schedules `clearScheduleSlot` set aside. */
export async function restoreSchedules(request: APIRequestContext, name: string, aside: SavedSchedule[]) {
  const headers = await writeHeaders(request)
  const list = await (await request.get('/api/offhours/schedules')).json()
  for (const item of (list.items ?? []) as { id: number; name: string }[]) {
    if (item.name === name) await deleteSchedule(request, item.id, headers)
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
    data: {
      name,
      enabled: true,
      tags: [{ kind: 'aws', key: tagKey, value: tagValue }],
      excluded_service_ids: [],
      timezone: 'UTC',
      awake: gridAwakeOnlyAt(awakeHourUtc()),
    },
  })
  expect(response.ok(), await response.text()).toBe(true)
  return (await response.json()).id as number
}

/** Tasks the service wants right now, from the service API. */
export async function desiredTasks(request: APIRequestContext, id: string) {
  return (await (await request.get(`/api/services/${id}`)).json()).desired as number
}

/** Opens the new-schedule editor and fills name, one AWS tag (`key=value`) and UTC. Leaves the grid empty. */
export async function startSchedule(page: Page, name: string, tag: string) {
  await page.goto('/settings/offhours')
  await page.getByRole('button', { name: 'Add schedule' }).first().click()
  await page.getByLabel('Name', { exact: true }).fill(name)
  await page.getByRole('button', { name: 'Services tagged', exact: true }).click()
  const options = page.getByRole('group', { name: 'Services tagged options' })
  await options.getByRole('searchbox', { name: 'Search Services tagged' }).fill(tag)
  // The tag is ticked from the list when a service carries it, else added as typed.
  const listed = options.getByRole('checkbox', { name: new RegExp(`^${tag.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}\\b`) })
  const add = options.getByRole('button', { name: `Add ${tag}` })
  await listed.or(add).first().click()
  await page.keyboard.press('Escape')
  await page.getByLabel('Timezone').selectOption('UTC')
}
