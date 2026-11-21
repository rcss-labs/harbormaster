import { expect, type APIRequestContext, type Page } from '@playwright/test'

/** What the in-process fake receiver recorded (GET /fake-notify/received). */
export interface Received {
  time: string
  path: string
  headers: Record<string, string>
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  body: any
}

export const ROUTE_PREFIX = 'e2e '

export async function received(request: APIRequestContext): Promise<Received[]> {
  const response = await request.get('/fake-notify/received')
  expect(response.ok(), await response.text()).toBe(true)
  return (await response.json()) as Received[]
}

/** What one path of the receiver got since `since`. */
export async function receivedAt(request: APIRequestContext, path: string, since: Date): Promise<Received[]> {
  return (await received(request)).filter((entry) => entry.path === path && new Date(entry.time) >= since)
}

async function writeHeaders(request: APIRequestContext) {
  const me = (await (await request.get('/api/me')).json()) as { csrf_token: string }
  return { 'X-CSRF-Token': me.csrf_token }
}

/** Deletes every route whose name starts with "e2e ", so a failed run leaves nothing behind. */
export async function deleteE2eRoutes(request: APIRequestContext) {
  const headers = await writeHeaders(request)
  const list = (await (await request.get('/api/notifications/routes')).json()) as { items: { id: number; name: string }[] }
  for (const route of list.items.filter((item) => item.name.startsWith(ROUTE_PREFIX))) {
    const response = await request.delete(`/api/notifications/routes/${route.id}`, { headers })
    expect(response.ok(), await response.text()).toBe(true)
  }
}

/** Creates a route through the API (for the specs that are not about the form). */
export async function createRoute(request: APIRequestContext, body: Record<string, unknown>) {
  const headers = await writeHeaders(request)
  const response = await request.post('/api/notifications/routes', { headers, data: body })
  expect(response.ok(), await response.text()).toBe(true)
  return (await response.json()) as { id: number }
}

export const KINDS = ['service_failing', 'service_degraded', 'restart_loop', 'unhealthy_task', 'deployment_failed', 'cannot_place', 'account_unreachable']

/** The fixed part of a webhook route body; callers add name, url and scope. */
export function webhookBody(name: string, url: string, extra: Record<string, unknown> = {}) {
  return {
    name,
    enabled: true,
    destination: 'webhook',
    label: '',
    recipients: [],
    pagerduty_region: '',
    chat_id: '',
    thread_id: null,
    url,
    min_severity: 'warning',
    kinds: KINDS,
    repeat_seconds: 0,
    quiet_enabled: false,
    quiet_timezone: 'UTC',
    notify_resolved: false,
    scope: [],
    fallback: false,
    ...extra,
  }
}

export async function openNotifications(page: Page) {
  await page.goto('/settings/notifications')
  await expect(page.getByRole('heading', { level: 2, name: 'Alert notifications' })).toBeVisible()
}

/** Starts a new route in the editor and fills the fields every destination shares. */
export async function startRoute(page: Page, name: string, destination: 'Webhook' | 'Telegram') {
  await page.getByRole('button', { name: 'Add route' }).click()
  const nameField = page.getByLabel('Route name')
  await expect(nameField).toBeVisible()
  await nameField.fill(name)
  await page.getByLabel('Send to').selectOption({ label: destination })
  await page.getByLabel('Send matching alerts').selectOption({ label: 'Warning and critical' })
}

/** Picks one entry in the scope combobox by what its option starts with. */
export async function pickScope(page: Page, query: string, option: RegExp) {
  const box = page.getByRole('combobox', { name: 'Add to scope' })
  await box.fill(query)
  await page.getByRole('option', { name: option }).first().click()
}

export const routeRow = (page: Page, name: string) => page.getByRole('list', { name: 'Routes' }).getByRole('button', { name: new RegExp(name) })

/** The Delivery log view, optionally with the failed filter. */
export async function openLog(page: Page) {
  await page.getByRole('button', { name: 'Delivery log', exact: true }).click()
  await expect(page.getByRole('table', { name: 'Deliveries' })).toBeVisible()
}

export async function failingServices(request: APIRequestContext): Promise<{ cluster: string; service: string; alerts: number }[]> {
  const alerts = (await (await request.get('/api/alerts')).json()) as {
    items: { severity: string; service?: { name: string } | string; cluster?: { name: string } | string; title: string }[]
  }
  const name = (value: { name: string } | string | undefined) => (typeof value === 'string' ? value : (value?.name ?? ''))
  const counts = new Map<string, { cluster: string; service: string; alerts: number }>()
  for (const alert of alerts.items.filter((item) => item.severity !== 'info')) {
    const service = name(alert.service)
    if (!service) continue // an account-level alert
    const cluster = name(alert.cluster)
    const key = `${cluster}/${service}`
    const entry = counts.get(key) ?? { cluster, service, alerts: 0 }
    entry.alerts++
    counts.set(key, entry)
  }
  return [...counts.values()]
}
