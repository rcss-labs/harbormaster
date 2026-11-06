import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { AlertDTO, AlertsDTO, ServiceDTO } from '../../api/types'
import { ALERTS, ME_OMER, SERVICE_DETAILS } from '../../test/fixtures'
import { server } from '../../test/msw'
import { renderApp } from '../../test/renderApp'

beforeEach(() => localStorage.clear())

const panel = async (): Promise<HTMLElement> => {
  const region = await screen.findByRole('region', { name: 'Needs attention' })
  await within(region).findByText(ALERTS.items[0].title)
  return region
}
const rowsOf = (region: HTMLElement) => Array.from(region.querySelectorAll<HTMLElement>('[data-testid="alert-row"]'))

const resizer = ALERTS.items[0]
const resizerDetail = SERVICE_DETAILS[resizer.service_id]

/** n distinct failing-service alerts, ordered worst first like the API. */
const manyAlerts = (count: number): AlertsDTO => ({
  ...ALERTS,
  items: Array.from(
    { length: count },
    (_, i): AlertDTO => ({
      ...resizer,
      title: `svc-${i}: 0 of 2 tasks running`,
      service_id: `svc-${i}`,
      service: `svc-${i}`,
      at: new Date(Date.UTC(2026, 0, 1, 0, i)).toISOString(),
    }),
  ),
  counts: { danger: count, warning: 0, info: 0 },
})

describe('NeedsAttention', () => {
  it('lists one row per service, worst first, with status text and the open count', async () => {
    renderApp('/')
    const region = await panel()
    const rows = rowsOf(region)
    expect(rows).toHaveLength(4)
    expect(within(rows[0]).getByText('Failing')).toBeInTheDocument()
    expect(within(rows[2]).getByText('Warning')).toBeInTheDocument()
    expect(within(rows[3]).getByText('In progress')).toBeInTheDocument()
    expect(within(region).getByText('4 open')).toBeInTheDocument()
  })

  it('names the cluster on each row and links the title to the service tab', async () => {
    renderApp('/')
    const rows = rowsOf(await panel())
    expect(within(rows[0]).getByText(/background-jobs/)).toBeInTheDocument()
    expect(within(rows[0]).getByRole('link', { name: resizer.title })).toHaveAttribute('href', `/services/${resizer.service_id}/tasks`)
    expect(within(rows[3]).getByRole('link', { name: ALERTS.items[3].title })).toHaveAttribute(
      'href',
      `/services/${ALERTS.items[3].service_id}/overview`,
    )
  })

  it('links account alerts to /settings/accounts for admins and offers no service actions on them', async () => {
    renderApp('/')
    const rows = rowsOf(await panel())
    expect(within(rows[1]).getByRole('link', { name: ALERTS.items[1].title })).toHaveAttribute('href', '/settings/accounts')
    expect(within(rows[1]).queryByRole('button', { name: 'Restart' })).not.toBeInTheDocument()
  })

  it('offers Logs and Tasks links to the service', async () => {
    renderApp('/')
    const row = rowsOf(await panel())[0]
    expect(within(row).getByRole('link', { name: 'Logs' })).toHaveAttribute('href', `/services/${resizer.service_id}/logs`)
    expect(within(row).getByRole('link', { name: 'Tasks' })).toHaveAttribute('href', `/services/${resizer.service_id}/tasks`)
  })

  it('shows cause, next step and just before', async () => {
    renderApp('/')
    const row = rowsOf(await panel())[0]
    expect(within(row).getByText(resizer.cause)).toBeInTheDocument()
    expect(within(row).getByRole('link', { name: resizer.next_step.label })).toHaveAttribute('href', resizer.next_step.href)
    expect(within(row).getByText(`Just before: ${resizer.just_before?.text}`)).toBeInTheDocument()
  })

  it('shows no Just before line when the API omits it', async () => {
    renderApp('/')
    const rows = rowsOf(await panel())
    expect(rows[3]).toHaveTextContent(ALERTS.items[3].cause)
    expect(rows[3]).not.toHaveTextContent('Just before')
    expect(rows[1]).not.toHaveTextContent('Just before')
  })

  it('shows no Next step link when the API has no href', async () => {
    server.use(
      http.get('/api/alerts', () =>
        HttpResponse.json({ ...ALERTS, items: [{ ...resizer, next_step: { label: '', href: '' } }], counts: { danger: 1, warning: 0, info: 0 } }),
      ),
    )
    renderApp('/')
    const row = rowsOf(await panel())[0]
    expect(within(row).queryByText(/Next step/)).not.toBeInTheDocument()
  })

  it('account problem row links to settings', async () => {
    renderApp('/')
    const row = rowsOf(await panel())[1]
    expect(within(row).getByRole('link', { name: 'Open accounts' })).toHaveAttribute('href', '/settings/accounts')
  })

  it('filters by severity and shows the counts on the chips', async () => {
    const user = userEvent.setup()
    renderApp('/')
    const region = await panel()
    expect(within(region).getByRole('radio', { name: 'All 4' })).toBeChecked()
    await user.click(within(region).getByRole('radio', { name: 'Warning 1' }))
    const rows = rowsOf(region)
    expect(rows).toHaveLength(1)
    expect(rows[0]).toHaveTextContent(ALERTS.items[2].title)
    await user.click(within(region).getByRole('radio', { name: 'In progress 1' }))
    expect(rowsOf(region)[0]).toHaveTextContent(ALERTS.items[3].title)
  })

  it('caps the list at 6 rows until Show all N is pressed', async () => {
    server.use(http.get('/api/alerts', () => HttpResponse.json(manyAlerts(9))))
    const user = userEvent.setup()
    renderApp('/')
    const region = await screen.findByRole('region', { name: 'Needs attention' })
    await within(region).findByRole('button', { name: 'Show all 9' })
    expect(rowsOf(region)).toHaveLength(6)
    await user.click(within(region).getByRole('button', { name: 'Show all 9' }))
    expect(rowsOf(region)).toHaveLength(9)
    await user.click(within(region).getByRole('button', { name: 'Show fewer' }))
    expect(rowsOf(region)).toHaveLength(6)
  })

  it('shows no Show all control when 6 or fewer rows exist', async () => {
    renderApp('/')
    const region = await panel()
    expect(within(region).queryByRole('button', { name: /Show all/ })).not.toBeInTheDocument()
  })

  it('says so when nothing needs attention', async () => {
    server.use(http.get('/api/alerts', () => HttpResponse.json({ items: [], counts: { danger: 0, warning: 0, info: 0 }, polled_at: null })))
    renderApp('/')
    expect(await screen.findByText('Nothing needs attention.')).toBeInTheDocument()
    expect(screen.getByText('0 open')).toBeInTheDocument()
  })
})

describe('NeedsAttention Restart', () => {
  const useDetail = (detail: ServiceDTO) => server.use(http.get(`/api/services/${detail.id}`, () => HttpResponse.json(detail)))

  it('opens the confirm dialog, then posts the restart once and toasts', async () => {
    const posted = vi.fn()
    server.use(
      http.post(`/api/services/${resizer.service_id}/restart`, () => {
        posted()
        return HttpResponse.json({ deployment_id: 'dep-1', revision: resizerDetail.revision, kind: 'restart' }, { status: 202 })
      }),
    )
    const user = userEvent.setup()
    renderApp('/')
    const row = rowsOf(await panel())[0]
    const restart = within(row).getByRole('button', { name: 'Restart' })
    await waitFor(() => expect(restart).toBeEnabled())
    await user.click(restart)
    const dialog = await screen.findByRole('dialog')
    expect(dialog).toHaveTextContent(`Restart ${resizerDetail.name}?`)
    expect(posted).not.toHaveBeenCalled()
    await user.click(within(dialog).getByRole('button', { name: 'Restart' }))
    await waitFor(() => expect(posted).toHaveBeenCalledTimes(1))
    expect(await screen.findByText('Restart started.')).toBeInTheDocument()
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
  })

  it('cancelling the dialog posts nothing', async () => {
    const posted = vi.fn()
    server.use(
      http.post(`/api/services/${resizer.service_id}/restart`, () => {
        posted()
        return HttpResponse.json({}, { status: 202 })
      }),
    )
    const user = userEvent.setup()
    renderApp('/')
    const row = rowsOf(await panel())[0]
    await waitFor(() => expect(within(row).getByRole('button', { name: 'Restart' })).toBeEnabled())
    await user.click(within(row).getByRole('button', { name: 'Restart' }))
    await user.click(within(await screen.findByRole('dialog')).getByRole('button', { name: 'Cancel' }))
    expect(posted).not.toHaveBeenCalled()
  })

  it('disables Restart without Operate and names the tag in the title', async () => {
    useDetail({ ...resizerDetail, can: { ...resizerDetail.can, operate: false } })
    renderApp('/')
    const restart = within(rowsOf(await panel())[0]).getByRole('button', { name: 'Restart' })
    await waitFor(() => expect(restart).toHaveAttribute('title', `You need Operate on ${resizerDetail.tag.name}`))
    expect(restart).toBeDisabled()
  })

  it('disables Restart while a rollout is running', async () => {
    useDetail({ ...resizerDetail, deployments: [{ ...resizerDetail.deployments[0], rollout_state: 'IN_PROGRESS' }] })
    renderApp('/')
    const restart = within(rowsOf(await panel())[0]).getByRole('button', { name: 'Restart' })
    await waitFor(() => expect(restart).toHaveAttribute('title', 'A rollout is already running'))
    expect(restart).toBeDisabled()
  })

  it('disables Restart when the controller does not support it, using its banner', async () => {
    useDetail({ ...resizerDetail, controller: { ...resizerDetail.controller, supported: false, banner: 'Managed by the ECS controller.' } })
    renderApp('/')
    const restart = within(rowsOf(await panel())[0]).getByRole('button', { name: 'Restart' })
    await waitFor(() => expect(restart).toHaveAttribute('title', 'Managed by the ECS controller.'))
    expect(restart).toBeDisabled()
  })
})

describe('NeedsAttention for non-admins', () => {
  it('does not link account alerts', async () => {
    server.use(http.get('/api/me', () => HttpResponse.json(ME_OMER)))
    renderApp('/')
    const rows = rowsOf(await panel())
    expect(within(rows[1]).queryByRole('link', { name: ALERTS.items[1].title })).not.toBeInTheDocument()
    expect(rows[1]).toHaveTextContent(ALERTS.items[1].title)
    expect(within(rows[1]).queryByRole('link', { name: 'Open accounts' })).not.toBeInTheDocument()
  })
})
