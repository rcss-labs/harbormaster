import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { beforeEach, describe, expect, it } from 'vitest'
import type { OffHoursPreview, OffHoursPreviewRequest, OffHoursScheduleInput, OffHoursService } from '../../../api/offhours'
import { ME_OMER } from '../../../test/fixtures'
import { server } from '../../../test/msw'
import {
  OFFHOURS_ASLEEP,
  OFFHOURS_LIST,
  OFFHOURS_PREVIEW_PRODUCTION,
  OFFHOURS_PREVIEW_STAGING,
  OFFHOURS_STAGING,
  OFFHOURS_STAGING_DETAIL,
  OFFHOURS_UNPRICED_DETAIL,
  WAKE_RESPONSE,
} from '../../../test/offhoursFixtures'
import { renderApp } from '../../../test/renderApp'

beforeEach(() => {
  localStorage.clear()
  window.history.replaceState(null, '', '/')
})

const SLOW = { timeout: 4000 }
const NOT_PRICED = 'Not priced: EC2 or unknown region'

const servicesTable = () => screen.findByRole('table', { name: 'Services on this schedule' })
const rowOf = async (service: string) => {
  const table = await servicesTable()
  const row = within(table)
    .getAllByRole('row')
    .find((candidate) => within(candidate).queryByText(service, { exact: true }))
  if (!row) throw new Error(`no row for ${service}`)
  return row
}
const servePreview = (preview: OffHoursPreview | ((body: OffHoursPreviewRequest) => OffHoursPreview)) =>
  server.use(
    http.post('/api/offhours/preview', async ({ request }) => {
      const body = (await request.json()) as OffHoursPreviewRequest
      return HttpResponse.json(typeof preview === 'function' ? preview(body) : preview)
    }),
  )
const serveDetailServices = (services: OffHoursService[]) =>
  server.use(http.get('/api/offhours/schedules/1', () => HttpResponse.json({ ...OFFHOURS_STAGING_DETAIL, services })))

describe('OffHoursPage', () => {
  it('renders KPIs, list and services from the fixture', async () => {
    renderApp('/settings/offhours')
    expect(await screen.findByRole('heading', { level: 2, name: 'Off-hours schedules' })).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Off-hours' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Add schedule' })).toBeInTheDocument()

    expect(await screen.findByLabelText('Name')).toHaveValue('Staging nights and weekends')
    const kpis = screen.getByRole('list', { name: 'Off-hours savings' })
    expect(within(kpis).getByText('Estimated saving a month, all schedules').parentElement).toHaveTextContent('$25')
    expect(within(kpis).getByText('Awake a week on this schedule (of 168)').parentElement).toHaveTextContent('65 h')
    expect(within(kpis).getByText('Saving a month on this schedule')).toBeInTheDocument()

    const list = screen.getByRole('list', { name: 'Schedules' })
    expect(within(list).getByText('Staging nights and weekends')).toBeInTheDocument()
    expect(within(list).getByText('env=staging · 65 h awake a week')).toBeInTheDocument()
    expect(within(list).getByText('env=preview · 45 h awake a week')).toBeInTheDocument()
    expect(within(list).getByText('On')).toBeInTheDocument()
    expect(within(list).getByText('Off')).toBeInTheDocument()

    expect(screen.getByLabelText('Enabled')).toBeChecked()
    expect(screen.getByLabelText('Timezone')).toHaveValue('Europe/Berlin')
    expect(screen.getByText('Blue hours are awake. Times in Europe/Berlin. Click or tab to a cell to toggle it.')).toBeInTheDocument()
    expect(screen.getByText(/Auto scaling policies stay in place\./)).toBeInTheDocument()

    const web = await rowOf('web')
    expect(within(web).getByText('staging-web')).toBeInTheDocument()
    expect(within(web).getByText('1 → 0')).toBeInTheDocument()
    expect(within(web).getByText('Asleep until Mon 07:00')).toBeInTheDocument()
    expect(within(web).getByText('$9')).toBeInTheDocument()
    expect(screen.getByText('Now: Sat 22:00')).toBeInTheDocument()
    const api = await rowOf('api')
    expect(within(api).getByText('2 → 2')).toBeInTheDocument()
  })

  it('preset changes awake hours and the saving KPI after preview', async () => {
    const bodies: OffHoursPreviewRequest[] = []
    servePreview((body) => {
      bodies.push(body)
      return { ...OFFHOURS_PREVIEW_STAGING, monthly_saving: body.awake.flat().filter(Boolean).length === 50 ? 40 : 24.7 }
    })
    renderApp('/settings/offhours')
    const kpis = await screen.findByRole('list', { name: 'Off-hours savings' })
    await userEvent.click(await screen.findByRole('button', { name: 'Weekdays 08–18' }))
    expect(within(kpis).getByText('Awake a week on this schedule (of 168)').parentElement).toHaveTextContent('50 h')
    await waitFor(() => expect(within(kpis).getByText('Saving a month on this schedule').parentElement).toHaveTextContent('$40'), SLOW)
    expect(bodies.at(-1)).toMatchObject({ schedule_id: 1, tag_key: 'env', tag_value: 'staging', timezone: 'Europe/Berlin' })
    expect(await screen.findByRole('button', { name: 'Mon 8:00 awake' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Mon 7:00 asleep' })).toBeInTheDocument()
  })

  it('warns when matched services look like production but still saves', async () => {
    servePreview(OFFHOURS_PREVIEW_PRODUCTION)
    const puts: OffHoursScheduleInput[] = []
    server.use(
      http.put('/api/offhours/schedules/:id', async ({ request }) => {
        puts.push((await request.json()) as OffHoursScheduleInput)
        return HttpResponse.json(OFFHOURS_STAGING_DETAIL)
      }),
    )
    renderApp('/settings/offhours')
    const warning = await screen.findByText('1 matched service looks like production:', undefined, SLOW)
    const banner = warning.closest('[data-hm-banner]') as HTMLElement
    expect(banner).toHaveAttribute('data-hm-banner', 'warn')
    expect(within(banner).getByText('core-services/payments-api')).toBeInTheDocument()
    expect(within(banner).getByText('AWS tag env=prod; Harbormaster tag "Prod"')).toBeInTheDocument()
    expect(screen.getByText('Schedule "Staging nights and weekends" already matches 1 of these services: core-services/payments-api.')).toBeInTheDocument()

    const save = screen.getByRole('button', { name: 'Save' })
    expect(save).toBeEnabled()
    await userEvent.click(save)
    await waitFor(() => expect(puts).toHaveLength(1))
    expect(puts[0]).toMatchObject({ name: 'Staging nights and weekends', version: 1, tag_key: 'env', tag_value: 'staging', enabled: true })
    expect(await screen.findByText('Saved.')).toBeInTheDocument()
  })

  it('shows the overlap error from the server and keeps the edits', async () => {
    const message = 'core-services/payments-api already follows "Preview environments". A service can follow one schedule.'
    server.use(http.put('/api/offhours/schedules/:id', () => HttpResponse.json({ error: { code: 'conflict', message } }, { status: 409 })))
    renderApp('/settings/offhours')
    const name = await screen.findByLabelText('Name')
    await userEvent.clear(name)
    await userEvent.type(name, 'Staging nights')
    await userEvent.click(screen.getByRole('button', { name: 'Weekdays 08–18' }))
    await userEvent.click(screen.getByRole('button', { name: 'Save' }))
    expect(await screen.findByRole('alert')).toHaveTextContent(message)
    expect(screen.getByLabelText('Name')).toHaveValue('Staging nights')
    expect(screen.getByRole('button', { name: 'Mon 8:00 awake' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Save' })).toBeEnabled()
  })

  it('shows the stale-version error from the server and keeps the edits', async () => {
    const message = 'Someone else changed this schedule. Reload it, then apply your edits again.'
    server.use(http.put('/api/offhours/schedules/:id', () => HttpResponse.json({ error: { code: 'stale_version', message } }, { status: 409 })))
    renderApp('/settings/offhours')
    const name = await screen.findByLabelText('Name')
    await userEvent.type(name, ' 2')
    await userEvent.click(screen.getByRole('button', { name: 'Save' }))
    expect(await screen.findByRole('alert')).toHaveTextContent(message)
    expect(screen.getByLabelText('Name')).toHaveValue('Staging nights and weekends 2')
  })

  it('says how many services will go to sleep on save', async () => {
    servePreview({ ...OFFHOURS_PREVIEW_STAGING, sleep_now: 3 })
    renderApp('/settings/offhours')
    expect(await screen.findByText('3 services will go to sleep when you save.', undefined, SLOW)).toBeInTheDocument()
  })

  it('names at most ten overlapping services, then how many more', async () => {
    const services = Array.from({ length: 12 }, (_, i) => `core/svc-${i + 1}`)
    servePreview({ ...OFFHOURS_PREVIEW_PRODUCTION, overlaps: [{ schedule_id: 2, schedule_name: 'Preview environments', services }] })
    renderApp('/settings/offhours')
    const names = services.slice(0, 10).join(', ')
    expect(await screen.findByText(`Schedule "Preview environments" already matches 12 of these services: ${names} and 2 more.`, undefined, SLOW)).toBeInTheDocument()
  })

  it('shows a plain dash with no title until the figures are loaded', async () => {
    server.use(http.get('/api/offhours/schedules', async () => {
      await new Promise((resolve) => setTimeout(resolve, 300))
      return HttpResponse.json(OFFHOURS_LIST)
    }))
    renderApp('/settings/offhours')
    const kpis = await screen.findByRole('list', { name: 'Off-hours savings' })
    const dashes = within(kpis).getAllByText('—')
    expect(dashes.length).toBeGreaterThan(0)
    for (const dash of dashes) expect(dash).not.toHaveAttribute('title')
    await waitFor(() => expect(within(kpis).getByText('Estimated saving a month, all schedules').parentElement).toHaveTextContent('$25'))
  })

  it('drops the preview when the tag stops being a valid key=value', async () => {
    servePreview({ ...OFFHOURS_PREVIEW_STAGING, sleep_now: 3 })
    renderApp('/settings/offhours')
    expect(await screen.findByText('3 services will go to sleep when you save.', undefined, SLOW)).toBeInTheDocument()
    await userEvent.clear(screen.getByLabelText('Services tagged'))
    await waitFor(() => expect(screen.queryByText('3 services will go to sleep when you save.')).not.toBeInTheDocument())
  })

  it('does not fetch the deleted schedule again while the list refreshes', async () => {
    let deleted = false
    let detailFetchesAfterDelete = 0
    server.use(
      http.get('/api/offhours/schedules', () => HttpResponse.json(deleted ? { ...OFFHOURS_LIST, items: [OFFHOURS_LIST.items[1]] } : OFFHOURS_LIST)),
      http.get('/api/offhours/schedules/1', () => {
        if (deleted) detailFetchesAfterDelete++
        return deleted ? HttpResponse.json({ error: { code: 'not_found', message: 'Not found.' } }, { status: 404 }) : HttpResponse.json(OFFHOURS_STAGING_DETAIL)
      }),
      http.delete('/api/offhours/schedules/:id', async () => {
        deleted = true
        return new HttpResponse(null, { status: 204 })
      }),
    )
    renderApp('/settings/offhours')
    await userEvent.click(await screen.findByRole('button', { name: 'Delete' }))
    await userEvent.click(within(await screen.findByRole('dialog')).getByRole('button', { name: 'Delete schedule' }))
    await waitFor(() => expect(screen.getByLabelText('Name')).toHaveValue('Preview environments'))
    expect(detailFetchesAfterDelete).toBe(0)
    expect(screen.queryByText('Could not load this schedule.')).not.toBeInTheDocument()
  })

  it('says nothing about sleeping when no service would sleep now', async () => {
    servePreview({ ...OFFHOURS_PREVIEW_STAGING, sleep_now: 0 })
    renderApp('/settings/offhours')
    expect(await screen.findByText('staging-web/web', { exact: false }, SLOW)).toBeInTheDocument()
    expect(screen.queryByText(/will go to sleep when you save/)).not.toBeInTheDocument()
  })

  it('wakes a sleeping service and shows Woken until', async () => {
    let woken = false
    const wakes: string[] = []
    server.use(
      http.get('/api/offhours/schedules/1', () =>
        HttpResponse.json({
          ...OFFHOURS_STAGING_DETAIL,
          services: [woken ? { ...OFFHOURS_ASLEEP, state: 'woken', until: WAKE_RESPONSE.woken_until } : OFFHOURS_ASLEEP],
        }),
      ),
      http.post('/api/services/:id/offhours/wake', ({ params }) => {
        wakes.push(String(params.id))
        woken = true
        return HttpResponse.json(WAKE_RESPONSE)
      }),
    )
    renderApp('/settings/offhours')
    const row = await rowOf('web')
    await userEvent.click(within(row).getByRole('button', { name: 'Wake for 2 hours' }))
    await waitFor(() => expect(wakes).toEqual(['staging-web-web']))
    expect(await screen.findByText('Woke web until 00:00.')).toBeInTheDocument()
    const after = await rowOf('web')
    await waitFor(() => expect(within(after).getByText('Woken until 00:00')).toBeInTheDocument())
    expect(within(after).getByRole('button', { name: 'Put back to sleep' })).toBeInTheDocument()
    expect(within(after).queryByRole('button', { name: 'Wake for 2 hours' })).not.toBeInTheDocument()
  })

  it('puts a woken service back to sleep', async () => {
    const sleeps: string[] = []
    server.use(
      http.post('/api/services/:id/offhours/sleep', ({ params }) => {
        sleeps.push(String(params.id))
        return HttpResponse.json({})
      }),
    )
    renderApp('/settings/offhours')
    const row = await rowOf('api')
    expect(within(row).getByText('Woken until 00:00')).toBeInTheDocument()
    expect(within(row).queryByRole('button', { name: 'Wake for 2 hours' })).not.toBeInTheDocument()
    await userEvent.click(within(row).getByRole('button', { name: 'Put back to sleep' }))
    await waitFor(() => expect(sleeps).toEqual(['staging-web-api']))
    expect(await screen.findByText('api is back to sleep.')).toBeInTheDocument()
  })

  it('hides wake for a user without Operate', async () => {
    serveDetailServices([{ ...OFFHOURS_ASLEEP, can_operate: false }])
    renderApp('/settings/offhours')
    const row = await rowOf('web')
    expect(within(row).getByText('Asleep until Mon 07:00')).toBeInTheDocument()
    expect(within(row).queryByRole('button', { name: 'Wake for 2 hours' })).not.toBeInTheDocument()
    expect(within(row).getByText('You need Operate on Staging to wake it.')).toBeInTheDocument()
  })

  it('explains a blocked service in a read-only account', async () => {
    renderApp('/settings/offhours')
    const row = await rowOf('worker')
    expect(within(row).getByText('Blocked')).toBeInTheDocument()
    expect(
      within(row).getByText(
        "Can't apply: Harbormaster's role in staging-example can't change auto scaling. The access module is probably deployed with read_only = true.",
      ),
    ).toBeInTheDocument()
    expect(within(row).queryByRole('button')).not.toBeInTheDocument()
  })

  it('shows not priced instead of $0', async () => {
    server.use(
      http.get('/api/offhours/schedules', () =>
        HttpResponse.json({ ...OFFHOURS_LIST, items: [{ ...OFFHOURS_UNPRICED_DETAIL, services: [], monthly_saving: null }], monthly_saving_total: null }),
      ),
    )
    servePreview({ ...OFFHOURS_PREVIEW_STAGING, monthly_saving: null })
    renderApp('/settings/offhours')
    const row = await rowOf('cert-renewer')
    const cost = within(row).getByText('—')
    expect(cost).toHaveAttribute('title', NOT_PRICED)
    expect(within(row).queryByText('$0')).not.toBeInTheDocument()
    const kpis = await screen.findByRole('list', { name: 'Off-hours savings' })
    await waitFor(() => expect(within(kpis).getAllByText('—')).toHaveLength(2), SLOW)
    for (const dash of within(kpis).getAllByText('—')) expect(dash).toHaveAttribute('title', NOT_PRICED)
  })

  it('shows the empty state when no service carries the tag', async () => {
    serveDetailServices([])
    renderApp('/settings/offhours')
    expect(await screen.findByText('No services carry this tag.')).toBeInTheDocument()
  })

  it('opens a blank editor for Add schedule and creates the schedule', async () => {
    const posts: OffHoursScheduleInput[] = []
    server.use(
      http.post('/api/offhours/schedules', async ({ request }) => {
        posts.push((await request.json()) as OffHoursScheduleInput)
        return HttpResponse.json({ ...OFFHOURS_STAGING_DETAIL, id: 9 }, { status: 201 })
      }),
    )
    renderApp('/settings/offhours')
    await screen.findByLabelText('Name')
    await userEvent.click(screen.getByRole('button', { name: 'Add schedule' }))
    await waitFor(() => expect(screen.getByLabelText('Name')).toHaveValue(''))
    expect(screen.queryByRole('button', { name: 'Delete' })).not.toBeInTheDocument()
    await userEvent.type(screen.getByLabelText('Name'), 'Dev nights')
    await userEvent.type(screen.getByLabelText('Services tagged'), 'env=dev')
    await userEvent.click(screen.getByRole('button', { name: 'Weekdays 07–20' }))
    await userEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(posts).toHaveLength(1))
    expect(posts[0]).toMatchObject({ name: 'Dev nights', tag_key: 'env', tag_value: 'dev', enabled: true })
    expect(posts[0].version).toBeUndefined()
  })

  it('offers the tags as options and accepts a typed key=value', async () => {
    renderApp('/settings/offhours')
    const tag = await screen.findByLabelText('Services tagged')
    expect(tag).toHaveValue('env=staging')
    await waitFor(() => expect(document.querySelectorAll('datalist option')).toHaveLength(3))
    expect(document.querySelector('datalist option[value="env=prod"]')).not.toBeNull()
  })

  it('asks before deleting, then deletes', async () => {
    const deletes: string[] = []
    server.use(
      http.delete('/api/offhours/schedules/:id', ({ params }) => {
        deletes.push(String(params.id))
        return new HttpResponse(null, { status: 204 })
      }),
    )
    renderApp('/settings/offhours')
    await userEvent.click(await screen.findByRole('button', { name: 'Delete' }))
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText('Services on this schedule get their saved capacity back.')).toBeInTheDocument()
    expect(deletes).toHaveLength(0)
    await userEvent.click(within(dialog).getByRole('button', { name: 'Cancel' }))
    expect(deletes).toHaveLength(0)
    await userEvent.click(screen.getByRole('button', { name: 'Delete' }))
    await userEvent.click(within(await screen.findByRole('dialog')).getByRole('button', { name: 'Delete schedule' }))
    await waitFor(() => expect(deletes).toEqual(['1']))
  })

  it('discards the edits', async () => {
    renderApp('/settings/offhours')
    const name = await screen.findByLabelText('Name')
    await userEvent.type(name, ' x')
    await userEvent.click(screen.getByRole('button', { name: 'Always on' }))
    expect(screen.getByLabelText('Name')).toHaveValue('Staging nights and weekends x')
    await userEvent.click(screen.getByRole('button', { name: 'Discard' }))
    expect(screen.getByLabelText('Name')).toHaveValue(OFFHOURS_STAGING.name)
    expect(screen.getByRole('button', { name: 'Mon 6:00 asleep' })).toBeInTheDocument()
  })

  it('non-admin sees the admin-only explanation', async () => {
    server.use(http.get('/api/me', () => HttpResponse.json(ME_OMER)))
    renderApp('/settings/offhours')
    expect(await screen.findByText('Only admins can manage off-hours schedules.')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Add schedule' })).not.toBeInTheDocument()
  })
})
