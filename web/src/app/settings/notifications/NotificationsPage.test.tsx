import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { beforeEach, describe, expect, it } from 'vitest'
import type { NotificationPreviewRequest, NotificationRouteInput } from '../../../api/notifications'
import { ME_ADMIN, ME_OMER } from '../../../test/fixtures'
import { server } from '../../../test/msw'
import {
  DELIVERY_FAILED,
  DELIVERY_HELD,
  NOTIFICATION_DELIVERIES,
  NOTIFICATION_PREVIEW,
  NOTIFICATION_PREVIEW_EMPTY,
  NOTIFICATION_PREVIEW_GROUPS,
  NOTIFICATION_PREVIEW_WEBHOOK,
  NOTIFICATION_ROUTES,
  NOTIFICATION_ROUTES_NO_KEY,
  NOTIFICATION_ROUTES_NO_SMTP,
  NOTIFICATION_SUMMARY_CLEAN,
  ROUTE_CORE_SLACK,
  ROUTE_RESIZER_TELEGRAM,
  ROUTE_WEBHOOK_SIGNED,
} from '../../../test/notificationsFixtures'
import { renderApp } from '../../../test/renderApp'

beforeEach(() => {
  localStorage.clear()
  window.history.replaceState(null, '', '/')
})

const SLOW = { timeout: 4000 }
const PAGE = '/settings/notifications'

const routeList = () => screen.findByRole('list', { name: 'Routes' })
const openRoute = async (name: string) => {
  const list = await routeList()
  await userEvent.click(within(list).getByRole('button', { name: new RegExp(name) }))
  await screen.findByLabelText('Route name')
}
/** The preview endpoint answers with a different body per destination, like the server. */
const servePreviews = (bodies: NotificationPreviewRequest[] = []) =>
  server.use(
    http.post('/api/notifications/preview', async ({ request }) => {
      const body = (await request.json()) as NotificationPreviewRequest
      bodies.push(body)
      return HttpResponse.json(body.destination === 'webhook' ? NOTIFICATION_PREVIEW_WEBHOOK : { ...NOTIFICATION_PREVIEW, destination: body.destination })
    }),
  )
const capture = <T,>(bodies: T[], reply: (body: T) => unknown) => async ({ request }: { request: Request }) => {
  const body = (await request.json()) as T
  bodies.push(body)
  return HttpResponse.json(reply(body) as Record<string, unknown>)
}
const addScopeEntry = async (query: string, option: RegExp) => {
  const box = screen.getByRole('combobox', { name: 'Add to scope' })
  await userEvent.clear(box)
  await userEvent.type(box, query)
  await userEvent.click(await screen.findByRole('option', { name: option }))
}

describe('NotificationsPage', () => {
  it('lists routes with destination, target and state', async () => {
    renderApp(PAGE)
    const list = await routeList()
    expect(within(list).getByText('Prod incidents to on-call')).toBeInTheDocument()
    expect(within(list).getByText('PagerDuty: On-call')).toBeInTheDocument()
    expect(within(list).getAllByText('Slack: hooks.slack.com').length).toBeGreaterThan(1)
    expect(within(list).getByText('Telegram: -1001234567890')).toBeInTheDocument()
    expect(within(list).getByText('Email: platform@example.com')).toBeInTheDocument()
    expect(within(list).getAllByText('On').length).toBeGreaterThan(3)
    expect(within(list).getAllByText('Off').length).toBeGreaterThan(0)
    expect(within(list).getByText('Enter the destination again.')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Notifications' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Add route' })).toBeInTheDocument()
  })

  it('shows Matches nothing, Catch-all and Failing pills in the list', async () => {
    renderApp(PAGE)
    const list = await routeList()
    expect(within(list).getByText('Catch-all')).toBeInTheDocument()
    expect(within(list).getByText('Matches nothing')).toBeInTheDocument()
    expect(within(list).getByText('Failing')).toBeInTheDocument()
    expect(within(list).getByText('core-services (2+)')).toBeInTheDocument()
    expect(within(list).getAllByText('Everything').length).toBeGreaterThan(0)
  })

  it('edits and saves a route, sending no secret that was not retyped', async () => {
    const puts: NotificationRouteInput[] = []
    server.use(http.put('/api/notifications/routes/:id', capture(puts, () => ({ ...ROUTE_CORE_SLACK, name: 'Renamed', version: 4 }))))
    renderApp(PAGE)
    await openRoute('core-services to #core-alerts')
    const name = screen.getByLabelText('Route name')
    expect(screen.getByRole('button', { name: 'Save route' })).toBeDisabled()
    await userEvent.clear(name)
    await userEvent.type(name, 'Renamed')
    await userEvent.click(screen.getByRole('button', { name: 'Save route' }))
    await waitFor(() => expect(puts).toHaveLength(1))
    expect(puts[0]).toMatchObject({ name: 'Renamed', destination: 'slack', version: 3 })
    for (const secret of ['url', 'routing_key', 'bot_token', 'signing_secret']) expect(puts[0]).not.toHaveProperty(secret)
    expect(await screen.findByText('Saved "Renamed".')).toBeInTheDocument()
  })

  it('adds a route that starts off', async () => {
    renderApp(PAGE)
    await routeList()
    await userEvent.click(await screen.findByRole('button', { name: 'Add route' }))
    expect(await screen.findByText('Route added. It stays off until you enable it.')).toBeInTheDocument()
    expect(screen.getByLabelText('Route name')).toHaveValue('New route 1')
    expect(screen.getByLabelText('Route is on')).not.toBeChecked()
    expect(screen.getByRole('button', { name: 'Save route' })).toBeDisabled()
    expect(screen.getByText('Enter a Slack incoming webhook URL (https://hooks.slack.com/...).')).toBeInTheDocument()
    expect(within(await routeList()).getByText('Not saved yet')).toBeInTheDocument()
  })

  it('creates a new route with the typed destination', async () => {
    const posts: NotificationRouteInput[] = []
    server.use(http.post('/api/notifications/routes', capture(posts, (body) => ({ ...ROUTE_CORE_SLACK, id: 50, name: body.name }))))
    renderApp(PAGE)
    await userEvent.click(await screen.findByRole('button', { name: 'Add route' }))
    await userEvent.type(await screen.findByLabelText('Slack webhook URL'), 'https://hooks.slack.com/services/T/B/new')
    await userEvent.click(screen.getByRole('button', { name: 'Save route' }))
    await waitFor(() => expect(posts).toHaveLength(1))
    expect(posts[0]).toMatchObject({ name: 'New route 1', enabled: false, destination: 'slack', url: 'https://hooks.slack.com/services/T/B/new', min_severity: 'critical' })
    expect(posts[0]).not.toHaveProperty('version')
  })

  it('disables Send test until the route is saved', async () => {
    renderApp(PAGE)
    await openRoute('core-services to #core-alerts')
    expect(screen.getByRole('button', { name: 'Send test' })).toBeEnabled()
    await userEvent.type(screen.getByLabelText('Route name'), ' x')
    expect(screen.getByRole('button', { name: 'Send test' })).toBeDisabled()
    expect(screen.getByText('Save the route to send a test.')).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'Add route' }))
    expect(await screen.findByRole('button', { name: 'Send test' })).toBeDisabled()
  })

  it('sends a test and points to the delivery log', async () => {
    renderApp(PAGE)
    await openRoute('core-services to #core-alerts')
    await userEvent.click(screen.getByRole('button', { name: 'Send test' }))
    expect(await screen.findByText('Test sent to hooks.slack.com. See the delivery log.')).toBeInTheDocument()
  })

  it('shows a failed test as an error toast with the returned note', async () => {
    server.use(http.post('/api/notifications/routes/:id/test', () => HttpResponse.json({ ...DELIVERY_FAILED, event: 'test', note: 'HTTP 503' })))
    renderApp(PAGE)
    await openRoute('core-services to #core-alerts')
    await userEvent.click(screen.getByRole('button', { name: 'Send test' }))
    expect(await screen.findByText('HTTP 503')).toBeInTheDocument()
  })

  it('shows the server preview for each destination', async () => {
    servePreviews()
    renderApp(PAGE)
    await openRoute('core-services to #core-alerts')
    const card = await screen.findByLabelText('Message', undefined, SLOW)
    expect(card).toHaveTextContent('payments-api: service failing')
    await userEvent.selectOptions(screen.getByLabelText('Send to'), 'webhook')
    const code = await screen.findByLabelText('Webhook body', undefined, SLOW)
    expect(code).toHaveTextContent('"event": "alert.raised"')
    expect(screen.queryByLabelText('Message')).not.toBeInTheDocument()
  })

  it('says how many notifications the route would send, and warns when none', async () => {
    renderApp(PAGE)
    await openRoute('core-services to #core-alerts')
    expect(await screen.findByText('Right now this route would send 2 notifications.', undefined, SLOW)).toBeInTheDocument()
    server.use(http.post('/api/notifications/preview', () => HttpResponse.json(NOTIFICATION_PREVIEW_EMPTY)))
    await userEvent.type(screen.getByLabelText('Route name'), ' again')
    const warning = await screen.findByText('This route matches no open alerts. Check the severity, scope and alert types.', undefined, SLOW)
    expect(warning).toBeInTheDocument()
    expect(screen.getByText('Sample alert')).toBeInTheDocument()
  })

  it('the preview request carries the scope and the fallback flag and never a secret', async () => {
    const bodies: NotificationPreviewRequest[] = []
    servePreviews(bodies)
    renderApp(PAGE)
    await openRoute('Everything else')
    await waitFor(() => expect(bodies.length).toBeGreaterThan(0), SLOW)
    const last = bodies.at(-1)!
    expect(last).toMatchObject({ fallback: true, route_id: 5, scope: [], destination: 'slack' })
    for (const secret of ['url', 'routing_key', 'bot_token', 'signing_secret', 'version']) expect(last).not.toHaveProperty(secret)
  })

  it('shows the group preview: open entries, per-alert names, a below-threshold line and pending alerts', async () => {
    server.use(http.post('/api/notifications/preview', () => HttpResponse.json(NOTIFICATION_PREVIEW_GROUPS)))
    renderApp(PAGE)
    await openRoute('core-services to #core-alerts')
    expect(await screen.findByText('Right now this route would send 1 notification.', undefined, SLOW)).toBeInTheDocument()
    const lines = screen.getByRole('list', { name: 'What this route would send' })
    expect(within(lines).getByText('core-services: 2 of 5 services failing')).toBeInTheDocument()
    const below = within(lines).getByText('staging-example: 1 of 4 services failing, below 3. Nothing is sent.')
    expect(below.className).toMatch(/lineMuted/)
    expect(screen.getByText('1 more may follow once they have lasted 3 minutes.')).toBeInTheDocument()
  })

  it('picks an account, a cluster, a service and a tag as the scope and sends them in the body', async () => {
    const posts: NotificationRouteInput[] = []
    server.use(http.post('/api/notifications/routes', capture(posts, (body) => ({ ...ROUTE_CORE_SLACK, id: 51, name: body.name }))))
    renderApp(PAGE)
    await userEvent.click(await screen.findByRole('button', { name: 'Add route' }))
    await userEvent.type(await screen.findByLabelText('Slack webhook URL'), 'https://hooks.slack.com/services/T/B/x')
    await addScopeEntry('sandbox', /^sandbox-admin \(all regions\)/)
    await addScopeEntry('core-services', /^core-services/)
    await addScopeEntry('payments', /^payments-api · core-services/)
    await addScopeEntry('Staging', /^Staging/)
    const chips = screen.getByRole('list', { name: 'Alerts from' })
    expect(within(chips).getAllByRole('listitem')).toHaveLength(4)
    await userEvent.click(screen.getByRole('button', { name: 'Save route' }))
    await waitFor(() => expect(posts).toHaveLength(1))
    expect(posts[0].scope).toEqual([
      { kind: 'account', account_id: '482109375512', threshold: 1 },
      { kind: 'cluster', cluster_id: 'NDgyMTA5Mzc1NTEyfHVzLWVhc3QtMXxjb3JlLXNlcnZpY2Vz', threshold: 1 },
      { kind: 'service', service_id: 'core-services-payments-api' },
      { kind: 'harbormaster', tag_id: 2 },
    ])
  })

  it('adds a typed key=value as an AWS tag', async () => {
    renderApp(PAGE)
    await userEvent.click(await screen.findByRole('button', { name: 'Add route' }))
    const box = await screen.findByRole('combobox', { name: 'Add to scope' })
    await userEvent.type(box, 'team=web')
    await userEvent.click(await screen.findByRole('option', { name: /Add team=web as an AWS tag/ }))
    expect(within(screen.getByRole('list', { name: 'Alerts from' })).getByText('team=web')).toBeInTheDocument()
  })

  it('shows Everything when the scope is empty', async () => {
    renderApp(PAGE)
    await userEvent.click(await screen.findByRole('button', { name: 'Add route' }))
    expect(within(await screen.findByRole('list', { name: 'Alerts from' })).getByText('Everything')).toBeInTheDocument()
  })

  it('marks a selector whose target no longer exists and lets it be removed', async () => {
    const puts: NotificationRouteInput[] = []
    server.use(http.put('/api/notifications/routes/:id', capture(puts, () => ROUTE_WEBHOOK_SIGNED)))
    renderApp(PAGE)
    await openRoute('Audit webhook')
    const chips = screen.getByRole('list', { name: 'Alerts from' })
    expect(within(chips).getByText('retired-api · core-services · No longer exists')).toBeInTheDocument()
    await userEvent.click(within(chips).getByRole('button', { name: 'Remove retired-api · core-services' }))
    expect(within(chips).queryByText(/No longer exists/)).not.toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'Save route' }))
    await waitFor(() => expect(puts).toHaveLength(1))
    expect(puts[0].scope).toHaveLength(2)
    expect(puts[0].scope.map((entry) => entry.kind)).toEqual(['aws', 'harbormaster'])
  })

  it('shows a threshold input on cluster and account chips and not on service or tag chips', async () => {
    renderApp(PAGE)
    await openRoute('core-services to #core-alerts')
    expect(screen.getByRole('spinbutton', { name: 'Failing services needed for core-services' })).toHaveValue(2)
    expect(screen.getByText('when 2 or more services are failing')).toBeInTheDocument()
    await openRoute('Audit webhook')
    expect(screen.queryByRole('spinbutton')).not.toBeInTheDocument()
    await openRoute('Staging account by email')
    expect(screen.getByRole('spinbutton', { name: 'Failing services needed for staging-example (all regions)' })).toHaveValue(1)
    expect(screen.getByText('when any service is failing')).toBeInTheDocument()
  })

  it('sends the typed threshold in the body and in the preview request', async () => {
    const puts: NotificationRouteInput[] = []
    const previews: NotificationPreviewRequest[] = []
    server.use(http.put('/api/notifications/routes/:id', capture(puts, () => ROUTE_CORE_SLACK)))
    servePreviews(previews)
    renderApp(PAGE)
    await openRoute('core-services to #core-alerts')
    const input = screen.getByRole('spinbutton', { name: 'Failing services needed for core-services' })
    await userEvent.clear(input)
    await userEvent.type(input, '4')
    await waitFor(() => expect(previews.at(-1)?.scope).toEqual([expect.objectContaining({ kind: 'cluster', threshold: 4 })]), SLOW)
    await userEvent.click(screen.getByRole('button', { name: 'Save route' }))
    await waitFor(() => expect(puts).toHaveLength(1))
    expect(puts[0].scope).toEqual([expect.objectContaining({ kind: 'cluster', threshold: 4 })])
  })

  it('refuses to add the same cluster twice', async () => {
    renderApp(PAGE)
    await openRoute('core-services to #core-alerts')
    const box = screen.getByRole('combobox', { name: 'Add to scope' })
    await userEvent.type(box, 'core-services')
    const options = await screen.findAllByRole('option')
    expect(options.some((option) => /^core-services\b/.test(option.textContent ?? '') && !/·/.test(option.textContent?.split('sandbox')[0] ?? ''))).toBe(false)
    expect(within(screen.getByRole('list', { name: 'Alerts from' })).getAllByRole('listitem')).toHaveLength(1)
  })

  it('explains the two modes under the scope picker', async () => {
    renderApp(PAGE)
    await openRoute('core-services to #core-alerts')
    expect(
      screen.getByText(
        'Services and tags send one notification for each failure. Clusters and accounts send one notification when the number of failing services reaches the number you set.',
      ),
    ).toBeInTheDocument()
    expect(screen.getByText('Leave empty for every failure everywhere.')).toBeInTheDocument()
  })

  it('says scaling is not a failure, with the settle time from the API', async () => {
    renderApp(PAGE)
    await openRoute('core-services to #core-alerts')
    expect(
      screen.getByText(
        'Scaling up or down is not a failure and is never notified. A service with no running tasks, or fewer than it should have, is reported once it has stayed that way for 3 minutes.',
      ),
    ).toBeInTheDocument()
    server.use(http.get('/api/notifications/routes', () => HttpResponse.json({ ...NOTIFICATION_ROUTES, settle_seconds: 0 })))
    renderApp(PAGE)
    expect(await screen.findAllByLabelText('Route name')).not.toHaveLength(0)
    await waitFor(() => expect(screen.getAllByText(/Scaling up or down is not a failure/).length).toBe(1))
  })

  it('saves a catch-all route with the fallback flag', async () => {
    const puts: NotificationRouteInput[] = []
    server.use(http.put('/api/notifications/routes/:id', capture(puts, () => ROUTE_CORE_SLACK)))
    renderApp(PAGE)
    await openRoute('core-services to #core-alerts')
    await userEvent.click(screen.getByLabelText('Only when no other route matches'))
    expect(screen.getByText('Use this for a catch-all channel. It gets an alert only when no other enabled route matches it.')).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'Save route' }))
    await waitFor(() => expect(puts).toHaveLength(1))
    expect(puts[0].fallback).toBe(true)
  })

  it('shows Telegram fields and never renders a stored bot token', async () => {
    renderApp(PAGE)
    await openRoute('image-resizer to Telegram')
    expect(screen.getByLabelText('Chat ID')).toHaveValue(ROUTE_RESIZER_TELEGRAM.chat_id)
    expect(screen.getByLabelText('Topic ID (optional)')).toHaveValue('7')
    const token = screen.getByLabelText('Bot token')
    expect(token).toHaveValue('')
    expect(token).toHaveAttribute('placeholder', 'Saved. Type to replace.')
    expect(token).toHaveAttribute('type', 'password')
    expect(screen.getByText('Add the bot to the chat first. For a channel, make it an administrator.')).toBeInTheDocument()
  })

  it('tells the admin that one route is one Slack channel', async () => {
    renderApp(PAGE)
    await openRoute('core-services to #core-alerts')
    expect(
      screen.getByText("One route posts to one Slack channel, the channel this incoming webhook belongs to. For another channel, add another route with that channel's webhook."),
    ).toBeInTheDocument()
  })

  it('disables Email when SMTP is not configured', async () => {
    server.use(http.get('/api/notifications/routes', () => HttpResponse.json(NOTIFICATION_ROUTES_NO_SMTP)))
    renderApp(PAGE)
    await openRoute('core-services to #core-alerts')
    expect(screen.getByRole('option', { name: 'Email' })).toBeDisabled()
    expect(screen.getByText('Set HM_SMTP_HOST to send email.')).toBeInTheDocument()
  })

  it('asks for HM_SESSION_KEY when secrets are unavailable', async () => {
    server.use(http.get('/api/notifications/routes', () => HttpResponse.json(NOTIFICATION_ROUTES_NO_KEY)))
    renderApp(PAGE)
    await userEvent.click(await screen.findByRole('button', { name: 'Add route' }))
    expect((await screen.findAllByText('Set HM_SESSION_KEY before adding notification routes.')).length).toBeGreaterThan(0)
    expect(screen.getByRole('button', { name: 'Save route' })).toBeDisabled()
  })

  it('deletes a route after confirming', async () => {
    const deletes: string[] = []
    server.use(
      http.delete('/api/notifications/routes/:id', ({ params }) => {
        deletes.push(String(params.id))
        return new HttpResponse(null, { status: 204 })
      }),
    )
    renderApp(PAGE)
    await openRoute('core-services to #core-alerts')
    await userEvent.click(screen.getByRole('button', { name: 'Delete route' }))
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText('Delete "core-services to #core-alerts"?')).toBeInTheDocument()
    expect(within(dialog).getByText('Its delivery log entries stay.')).toBeInTheDocument()
    expect(deletes).toEqual([])
    await userEvent.click(within(dialog).getByRole('button', { name: 'Delete route' }))
    await waitFor(() => expect(deletes).toEqual(['2']))
    expect(await screen.findByText('Route deleted.')).toBeInTheDocument()
  })

  it('never renders a stored secret', async () => {
    renderApp(PAGE)
    await openRoute('core-services to #core-alerts')
    for (const field of screen.getAllByDisplayValue(/./)) {
      expect((field as HTMLInputElement).value).not.toMatch(/hooks\.slack\.com\/services|https?:\/\//)
    }
    expect(screen.getByLabelText('Slack webhook URL')).toHaveValue('')
    expect(screen.getByLabelText('Slack webhook URL')).toHaveAttribute('placeholder', 'Saved. Type to replace.')
    await openRoute('Audit webhook')
    expect(screen.getByLabelText('Signing secret (optional)')).toHaveValue('')
    expect(screen.getByLabelText('Remove the signing secret')).not.toBeChecked()
  })

  it('sends an empty signing secret to clear it', async () => {
    const puts: NotificationRouteInput[] = []
    server.use(http.put('/api/notifications/routes/:id', capture(puts, () => ROUTE_WEBHOOK_SIGNED)))
    renderApp(PAGE)
    await openRoute('Audit webhook')
    await userEvent.click(screen.getByLabelText('Remove the signing secret'))
    await userEvent.click(screen.getByRole('button', { name: 'Save route' }))
    await waitFor(() => expect(puts).toHaveLength(1))
    expect(puts[0].signing_secret).toBe('')
  })
})

const agentOn = () => server.use(http.get('/api/me', () => HttpResponse.json({ ...ME_ADMIN, agent_enabled: true })))
const SWITCH = 'Investigate with AI'

describe('NotificationsPage investigate switch', () => {
  it('switch absent when the agent is off', async () => {
    renderApp(PAGE)
    await openRoute('core-services to #core-alerts')
    expect(screen.queryByLabelText(SWITCH)).not.toBeInTheDocument()
    expect(within(await routeList()).queryByText('AI')).not.toBeInTheDocument()
  })

  it('saves the switch', async () => {
    agentOn()
    const puts: NotificationRouteInput[] = []
    server.use(http.put('/api/notifications/routes/:id', capture(puts, () => ({ ...ROUTE_CORE_SLACK, investigate: true, version: 4 }))))
    renderApp(PAGE)
    await openRoute('core-services to #core-alerts')
    expect(screen.getByLabelText(SWITCH)).not.toBeChecked()
    await userEvent.click(screen.getByLabelText(SWITCH))
    await userEvent.click(screen.getByRole('button', { name: 'Save route' }))
    await waitFor(() => expect(puts).toHaveLength(1))
    expect(puts[0]).toMatchObject({ investigate: true, version: 3 })
  })

  it('leaves the member out when it did not change', async () => {
    agentOn()
    const puts: NotificationRouteInput[] = []
    server.use(http.put('/api/notifications/routes/:id', capture(puts, () => ({ ...ROUTE_CORE_SLACK, version: 4 }))))
    renderApp(PAGE)
    await openRoute('core-services to #core-alerts')
    await userEvent.type(screen.getByLabelText('Route name'), '!')
    await userEvent.click(screen.getByRole('button', { name: 'Save route' }))
    await waitFor(() => expect(puts).toHaveLength(1))
    expect(puts[0]).not.toHaveProperty('investigate')
  })

  it('marks switched routes in the list', async () => {
    agentOn()
    server.use(http.get('/api/notifications/routes', () => HttpResponse.json({ ...NOTIFICATION_ROUTES, items: [{ ...ROUTE_CORE_SLACK, investigate: true }, ROUTE_RESIZER_TELEGRAM] })))
    renderApp(PAGE)
    const list = await routeList()
    expect(within(list).getAllByText('AI')).toHaveLength(1)
  })

  it('shows the unseen services line from the preview', async () => {
    agentOn()
    server.use(http.post('/api/notifications/preview', () => HttpResponse.json({ ...NOTIFICATION_PREVIEW, agent: { services_total: 5, services_unseen: 2 } })))
    renderApp(PAGE)
    await openRoute('core-services to #core-alerts')
    expect(screen.queryByText(/The agent cannot see/)).not.toBeInTheDocument()
    await userEvent.click(screen.getByLabelText(SWITCH))
    expect(await screen.findByText("The agent cannot see 2 of this route's 5 services. Change its access on Settings, AI agent.")).toBeInTheDocument()
  })

  it('says nothing about unseen services when the agent sees all of them', async () => {
    agentOn()
    server.use(http.post('/api/notifications/preview', () => HttpResponse.json({ ...NOTIFICATION_PREVIEW, agent: { services_total: 5, services_unseen: 0 } })))
    renderApp(PAGE)
    await openRoute('core-services to #core-alerts')
    await userEvent.click(screen.getByLabelText(SWITCH))
    await screen.findByText('Preview')
    expect(screen.queryByText(/The agent cannot see/)).not.toBeInTheDocument()
  })

  it('shows a saved switch as on and disabled while the agent is off', async () => {
    const bodies: NotificationPreviewRequest[] = []
    server.use(
      http.get('/api/notifications/routes', () => HttpResponse.json({ ...NOTIFICATION_ROUTES, items: [{ ...ROUTE_CORE_SLACK, investigate: true }] })),
    )
    servePreviews(bodies)
    renderApp(PAGE)
    await openRoute('core-services to #core-alerts')
    const toggle = screen.getByLabelText(SWITCH)
    expect(toggle).toBeChecked()
    expect(toggle).toBeDisabled()
    expect(screen.getByText('The AI agent is off.')).toBeInTheDocument()
    await waitFor(() => expect(bodies.length).toBeGreaterThan(0))
    for (const body of bodies) expect(body).not.toHaveProperty('investigate')
  })

  it('says investigations are skipped until a model is set', async () => {
    agentOn()
    server.use(
      http.get('/api/agent/settings', () => HttpResponse.json({ role_models: { chat: { provider: 'a', model: 'b', available: true } }, catalogue: { providers: [] } })),
      http.get('/api/notifications/routes', () => HttpResponse.json({ ...NOTIFICATION_ROUTES, items: [{ ...ROUTE_CORE_SLACK, investigate: true }] })),
    )
    renderApp(PAGE)
    await openRoute('core-services to #core-alerts')
    expect(await screen.findByText(/Investigations are skipped until a model is set/)).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Settings, AI agent' })).toHaveAttribute('href', '/settings/agent')
    expect(screen.getByLabelText(SWITCH)).toBeEnabled()
  })

  it('shows no model line when the investigator model is set', async () => {
    agentOn()
    server.use(
      http.get('/api/agent/settings', () => HttpResponse.json({ role_models: { investigator: { provider: 'a', model: 'b', available: true } }, catalogue: { providers: [] } })),
      http.get('/api/notifications/routes', () => HttpResponse.json({ ...NOTIFICATION_ROUTES, items: [{ ...ROUTE_CORE_SLACK, investigate: true }] })),
    )
    renderApp(PAGE)
    await openRoute('core-services to #core-alerts')
    await screen.findByText('Preview')
    expect(screen.queryByText(/Investigations are skipped/)).not.toBeInTheDocument()
  })

  it('shows the PagerDuty note', async () => {
    agentOn()
    renderApp(PAGE)
    await openRoute('Prod incidents to on-call')
    expect(screen.queryByText(/PagerDuty gets no diagnosis message/)).not.toBeInTheDocument()
    await userEvent.click(screen.getByLabelText(SWITCH))
    expect(screen.getByText("PagerDuty gets no diagnosis message. The incident's link opens the investigation.")).toBeInTheDocument()
  })

  it('shows the webhook note', async () => {
    agentOn()
    renderApp(PAGE)
    await openRoute(ROUTE_WEBHOOK_SIGNED.name)
    await userEvent.click(screen.getByLabelText(SWITCH))
    expect(screen.getByText('Your receiver will get a new event value, diagnosis.')).toBeInTheDocument()
  })
})

describe('NotificationsPage delivery log', () => {
  it('labels a diagnosis row', async () => {
    server.use(http.get('/api/notifications/deliveries', () => HttpResponse.json({ ...NOTIFICATION_DELIVERIES, items: [{ ...DELIVERY_FAILED, event: 'diagnosis' }] })))
    renderApp(`${PAGE}?view=log`)
    const table = await screen.findByRole('table', { name: 'Deliveries' })
    expect(within(table).getByText('Diagnosis')).toBeInTheDocument()
    expect(within(table).getByRole('button', { name: 'Retry' })).toBeInTheDocument()
  })

  it('reads an unknown event as plain text without crashing', async () => {
    server.use(http.get('/api/notifications/deliveries', () => HttpResponse.json({ ...NOTIFICATION_DELIVERIES, items: [{ ...DELIVERY_FAILED, event: 'brand_new' }] })))
    renderApp(`${PAGE}?view=log`)
    expect(await screen.findByRole('table', { name: 'Deliveries' })).toBeInTheDocument()
  })

  it('reads a pending diagnosis with a future release time as held', async () => {
    const until = new Date(Date.now() + 3_600_000)
    const pad = (n: number) => String(n).padStart(2, '0')
    server.use(
      http.get('/api/notifications/deliveries', () =>
        HttpResponse.json({ ...NOTIFICATION_DELIVERIES, items: [{ ...DELIVERY_HELD, status: 'pending', event: 'diagnosis', held_until: until.toISOString() }] }),
      ),
    )
    renderApp(`${PAGE}?view=log`)
    const table = await screen.findByRole('table', { name: 'Deliveries' })
    expect(within(table).getByText('Held')).toBeInTheDocument()
    expect(within(table).getByText(`Held until ${pad(until.getHours())}:${pad(until.getMinutes())}`)).toBeInTheDocument()
  })

  it('shows the failed banner and opens the failed log from it', async () => {
    renderApp(PAGE)
    expect(await screen.findByText('2 deliveries failed in the last 24 hours. A route that keeps failing shows a warning on Settings.')).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'Open delivery log' }))
    expect(await screen.findByRole('table', { name: 'Deliveries' })).toBeInTheDocument()
    expect(screen.getByTestId('search')).toHaveTextContent('view=log&status=failed')
    expect(screen.getByRole('radio', { name: /Failed/ })).toBeChecked()
    expect(screen.getByText('image-resizer: restart loop')).toBeInTheDocument()
  })

  it('hides the banner when nothing failed', async () => {
    server.use(http.get('/api/notifications/summary', () => HttpResponse.json(NOTIFICATION_SUMMARY_CLEAN)))
    renderApp(PAGE)
    await routeList()
    expect(screen.queryByRole('button', { name: 'Open delivery log' })).not.toBeInTheDocument()
  })

  it('filters the log to failed and retries a failed delivery', async () => {
    const retried: string[] = []
    let delivered = false
    server.use(
      http.post('/api/notifications/deliveries/:id/retry', ({ params }) => {
        retried.push(String(params.id))
        delivered = true
        return HttpResponse.json({ ...DELIVERY_FAILED, status: 'pending', can_retry: false })
      }),
      http.get('/api/notifications/deliveries', ({ request }) => {
        const failedOnly = new URL(request.url).searchParams.get('status') === 'failed'
        const base = failedOnly ? { ...NOTIFICATIONS_FAILED } : { ...NOTIFICATION_DELIVERIES }
        const items = base.items.map((row) => (delivered && row.id === DELIVERY_FAILED.id ? { ...row, status: 'delivered' as const, can_retry: false, note: 'Delivered in 80 ms' } : row))
        return HttpResponse.json({ ...base, items })
      }),
    )
    renderApp(`${PAGE}?view=log`)
    const table = await screen.findByRole('table', { name: 'Deliveries' })
    expect(within(table).getAllByRole('row').length).toBeGreaterThan(3)
    await userEvent.click(screen.getByRole('radio', { name: /Failed/ }))
    await waitFor(() => expect(within(screen.getByRole('table', { name: 'Deliveries' })).getAllByRole('row')).toHaveLength(2))
    await userEvent.click(screen.getByRole('button', { name: 'Retry' }))
    await waitFor(() => expect(retried).toEqual([String(DELIVERY_FAILED.id)]))
    expect(await screen.findByText('Delivered on retry.', undefined, SLOW)).toBeInTheDocument()
  })

  it('shows held deliveries with their release time', async () => {
    renderApp(`${PAGE}?view=log`)
    const table = await screen.findByRole('table', { name: 'Deliveries' })
    const at = new Date(DELIVERY_HELD.held_until as string)
    const pad = (n: number) => String(n).padStart(2, '0')
    expect(within(table).getByText(`Held by quiet hours until ${pad(at.getHours())}:${pad(at.getMinutes())}`)).toBeInTheDocument()
    expect(within(table).getByText('Held')).toBeInTheDocument()
  })

  it('tags an update row and links an aggregated row to its cluster', async () => {
    renderApp(`${PAGE}?view=log`)
    const table = await screen.findByRole('table', { name: 'Deliveries' })
    const update = within(table)
      .getAllByRole('row')
      .find((row) => within(row).queryByText('core-services: 3 of 5 services failing'))
    expect(update).toBeDefined()
    expect(within(update!).getByText('Updated')).toBeInTheDocument()
    const link = within(update!).getByRole('link', { name: 'core-services: 3 of 5 services failing' })
    expect(link).toHaveAttribute('href', '/clusters/NDgyMTA5Mzc1NTEyfHVzLWVhc3QtMXxjb3JlLXNlcnZpY2Vz')
    const account = within(table).getByRole('link', { name: 'staging-example: 3 of 4 services failing' })
    expect(account.getAttribute('href')).toBe('/?account=731022849906&region=us-east-1')
    expect(within(table).getAllByRole('link', { name: 'payments-api: service failing' })[0]).toHaveAttribute('href', '/services/core-services-payments-api/overview')
  })

  it('says when there is nothing to show', async () => {
    server.use(http.get('/api/notifications/deliveries', () => HttpResponse.json({ items: [], next: null, polled_at: '2026-11-21T10:00:00Z' })))
    renderApp(`${PAGE}?view=log&status=failed`)
    expect(await screen.findByText('No failed deliveries.')).toBeInTheDocument()
  })

  it('loads more when the server has another page', async () => {
    const cursors: (string | null)[] = []
    server.use(
      http.get('/api/notifications/deliveries', ({ request }) => {
        const cursor = new URL(request.url).searchParams.get('cursor')
        cursors.push(cursor)
        return HttpResponse.json(cursor ? { ...NOTIFICATION_DELIVERIES, items: [{ ...DELIVERY_FAILED, id: 7, alert: 'older alert' }], next: null } : { ...NOTIFICATION_DELIVERIES, next: 'abc' })
      }),
    )
    renderApp(`${PAGE}?view=log`)
    await userEvent.click(await screen.findByRole('button', { name: 'Load more' }))
    expect(await screen.findByText('older alert')).toBeInTheDocument()
    expect(cursors).toContain('abc')
  })
})

describe('NotificationsPage for a non-admin', () => {
  it('shows the admin-only sentence', async () => {
    server.use(http.get('/api/me', () => HttpResponse.json(ME_OMER)))
    renderApp(PAGE)
    expect(await screen.findByText('Only admins can manage notification routes.')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Add route' })).not.toBeInTheDocument()
  })
})

const NOTIFICATIONS_FAILED = { ...NOTIFICATION_DELIVERIES, items: [DELIVERY_FAILED] }
