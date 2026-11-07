import { cleanup, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { LogsHistoryDTO } from '../../../api/types'
import { LOGS_HISTORY_PAYMENTS, SERVICE_PAYMENTS, minutesAgo } from '../../../test/fixtures'
import { server } from '../../../test/msw'
import { installPhoneMedia } from '../../../test/phone'
import { renderApp } from '../../../test/renderApp'

class MockSocket {
  readyState = 0
  onopen: (() => void) | null = null
  onmessage: ((event: { data: string }) => void) | null = null
  onclose: ((event: { code: number; reason: string }) => void) | null = null
  constructor(public url: string) {}
  send() {}
  close() {
    this.readyState = 3
  }
}

const route = `/services/${SERVICE_PAYMENTS.id}/logs`
const LONG = 'ERROR charge declined: card_expired for customer cus_000123 on attempt 3 of 3, giving up and refunding the hold'
const history: LogsHistoryDTO = {
  ...LOGS_HISTORY_PAYMENTS,
  lines: [
    { t: minutesAgo(5), container: 'app', task_id: 'a1b2c3d4e5f60718', message: 'GET /healthz 200 1ms' },
    { t: minutesAgo(3), container: 'app', task_id: 'a1b2c3d4e5f60718', message: LONG },
  ],
}

const serve = (dto: LogsHistoryDTO = history) => server.use(http.get('/api/services/:id/logs', () => HttpResponse.json(dto)))

describe('LogsTab on a phone', () => {
  let restore: () => void
  beforeEach(() => {
    restore = installPhoneMedia(true)
    vi.stubGlobal('WebSocket', MockSocket)
  })
  afterEach(() => {
    cleanup()
    restore()
    vi.unstubAllGlobals()
  })

  it('toolbar order', async () => {
    serve()
    renderApp(route)
    const container = await screen.findByLabelText('Container')
    const tail = screen.getByRole('group', { name: 'Tail' })
    const search = screen.getByLabelText('Search logs')
    const level = screen.getByRole('radiogroup', { name: 'Level' })
    const status = screen.getByText(/CloudWatch Logs/)
    const inOrder = [container, tail, search, level, status]
    inOrder.slice(1).forEach((el, index) => {
      expect(inOrder[index].compareDocumentPosition(el) & Node.DOCUMENT_POSITION_FOLLOWING, `element ${index + 1} follows element ${index}`).toBeTruthy()
    })
  })

  it('shows why an unsupported container cannot be picked, as text', async () => {
    serve(LOGS_HISTORY_PAYMENTS)
    renderApp(route)
    await screen.findByLabelText('Container')
    const notes = (await screen.findAllByRole('note')).map((note) => note.textContent)
    expect(notes).toContain('log-router: awsfirelens: Harbormaster cannot show these logs')
  })

  it('shows every Tail and Level option', async () => {
    serve()
    renderApp(route)
    const tail = await screen.findByRole('group', { name: 'Tail' })
    expect(within(tail).getAllByRole('button').map((b) => b.textContent)).toEqual(['Live', 'Paused'])
    const level = screen.getByRole('radiogroup', { name: 'Level' })
    expect(within(level).getAllByRole('radio').map((b) => b.textContent)).toEqual(['All', 'Warn+', 'Errors'])
  })

  it('row wraps message', async () => {
    serve()
    renderApp(route)
    const log = await screen.findByRole('log')
    const row = await within(log).findByRole('button', { name: new RegExp(LONG.slice(0, 30)) })
    const message = within(row).getByText(LONG)
    // The full message is the cell's content (it wraps), not an ellipsised cell that only carries it in a title.
    expect(message).not.toHaveAttribute('title')
    expect(row).toHaveAttribute('aria-haspopup', 'dialog')
  })

  it('line sheet shows the fields the DTO has', async () => {
    serve()
    renderApp(route)
    const log = await screen.findByRole('log')
    await userEvent.click(await within(log).findByRole('button', { name: new RegExp(LONG.slice(0, 30)) }))
    const sheet = await screen.findByRole('dialog', { name: 'Log line details' })
    const dl = within(sheet)
    expect(dl.getByText('Level').nextElementSibling).toHaveTextContent('error')
    expect(dl.getByText('Log group').nextElementSibling).toHaveTextContent('/ecs/payments-api')
    expect(dl.getByText('Log stream').nextElementSibling).toHaveTextContent('app/a1b2c3d4e5f60718')
    expect(dl.getByText('Task').nextElementSibling).toHaveTextContent('a1b2c3d4e5f60718')
    expect(dl.getByText('Container').nextElementSibling).toHaveTextContent('app')
    expect(dl.getByText('Time')).toBeInTheDocument()
    expect(sheet).toHaveTextContent(LONG)
    expect(dl.queryByText('Ingested')).not.toBeInTheDocument()
  })

  it('keeps the driver banners and Load older', async () => {
    serve()
    renderApp(route)
    expect(await screen.findByText('datadog-agent logs go to a different log group. Harbormaster cannot show those logs.')).toBeInTheDocument()
    expect(screen.getByText('log-router logs go to the awsfirelens driver. Harbormaster cannot show those logs.')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Load older' })).toBeInTheDocument()
  })
})
