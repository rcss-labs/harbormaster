import { act, fireEvent, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { LogsHistoryDTO } from '../../../api/types'
import { LOGS_HISTORY_PAYMENTS, SERVICE_PAYMENTS, minutesAgo } from '../../../test/fixtures'
import { server } from '../../../test/msw'
import { renderApp } from '../../../test/renderApp'

class MockSocket {
  static instances: MockSocket[] = []
  readyState = 0
  sent: string[] = []
  onopen: (() => void) | null = null
  onmessage: ((event: { data: string }) => void) | null = null
  onclose: ((event: { code: number; reason: string }) => void) | null = null
  constructor(public url: string) {
    MockSocket.instances.push(this)
  }
  send(data: string) {
    this.sent.push(data)
  }
  close() {
    this.readyState = 3
  }
}

const route = `/services/${SERVICE_PAYMENTS.id}/logs`
const supportedOnly: LogsHistoryDTO = {
  ...LOGS_HISTORY_PAYMENTS,
  containers: [LOGS_HISTORY_PAYMENTS.containers[0]],
}

function serveHistory(handler: (url: URL) => LogsHistoryDTO) {
  const seen: URL[] = []
  server.use(
    http.get('/api/services/:id/logs', ({ request }) => {
      const url = new URL(request.url)
      seen.push(url)
      return HttpResponse.json(handler(url))
    }),
  )
  return seen
}

beforeEach(() => {
  MockSocket.instances = []
  vi.stubGlobal('WebSocket', MockSocket)
})
afterEach(() => vi.unstubAllGlobals())

describe('LogsTab', () => {
  const mixedLevels: LogsHistoryDTO = {
    ...supportedOnly,
    lines: [
      { t: minutesAgo(5), container: 'app', task_id: 'a1b2c3d4e5f60718', message: 'GET /healthz 200 1ms' },
      { t: minutesAgo(4), container: 'app', task_id: 'a1b2c3d4e5f60718', message: 'WARN slow upstream: 2400ms' },
      { t: minutesAgo(3), container: 'app', task_id: 'a1b2c3d4e5f60718', message: 'ERROR charge declined: card_expired' },
    ],
  }

  it('renders each history line as one row: time, an unwrapped stream cell and the message, tinted by level', async () => {
    serveHistory(() => mixedLevels)
    renderApp(route)
    const log = await screen.findByRole('log')
    const rows = await within(log).findAllByRole('row')
    expect(rows).toHaveLength(3)

    const [info, warn, error] = rows
    expect(info).not.toHaveAttribute('data-level')
    expect(warn).toHaveAttribute('data-level', 'warn')
    expect(error).toHaveAttribute('data-level', 'error')

    // The stream column is one cell holding "container/task prefix" as a single text node, never split across lines.
    const cells = within(error).getAllByRole('cell')
    expect(cells).toHaveLength(3)
    expect(cells[1]).toHaveTextContent(/^app\/a1b2c3d4$/)
    expect(cells[1]).toHaveAttribute('title', 'app/a1b2c3d4e5f60718')
    expect(cells[2]).toHaveTextContent('ERROR charge declined: card_expired')
    expect(cells[2]).toHaveAttribute('title', 'ERROR charge declined: card_expired')
    expect(screen.getByText('/ecs/payments-api')).toBeInTheDocument()
  })

  it('level filter: Warn+ keeps warnings and errors, Errors keeps errors, All restores everything', async () => {
    serveHistory(() => mixedLevels)
    renderApp(route)
    const log = await screen.findByRole('log')
    await within(log).findByText('GET /healthz 200 1ms')
    const level = screen.getByRole('radiogroup', { name: 'Level' })
    expect(within(level).getByRole('radio', { name: 'All' })).toBeChecked()

    await userEvent.click(within(level).getByRole('radio', { name: 'Warn+' }))
    expect(within(log).queryByText('GET /healthz 200 1ms')).not.toBeInTheDocument()
    expect(within(log).getByText('WARN slow upstream: 2400ms')).toBeInTheDocument()
    expect(within(log).getByText('ERROR charge declined: card_expired')).toBeInTheDocument()

    await userEvent.click(within(level).getByRole('radio', { name: 'Errors' }))
    expect(within(log).queryByText('WARN slow upstream: 2400ms')).not.toBeInTheDocument()
    expect(within(log).getByText('ERROR charge declined: card_expired')).toBeInTheDocument()

    await userEvent.click(within(level).getByRole('radio', { name: 'All' }))
    expect(within(log).getByText('GET /healthz 200 1ms')).toBeInTheDocument()
  })

  it('starts Live and shows the tail status', async () => {
    serveHistory(() => supportedOnly)
    renderApp(route)
    await screen.findByRole('log')
    expect(screen.getByRole('button', { name: 'Live' })).toHaveAttribute('aria-pressed', 'true')
    expect(screen.getByRole('button', { name: 'Paused' })).toHaveAttribute('aria-pressed', 'false')
  })

  it('Load older requests before=older_before and prepends', async () => {
    const older = { ...supportedOnly, lines: [{ t: '2026-10-31T09:00:00Z', container: 'app', task_id: 'ffffffff00', message: 'older line' }], older_before: null }
    const seen = serveHistory((url) => (url.searchParams.get('before') ? older : supportedOnly))
    renderApp(route)
    const log = await screen.findByRole('log')
    await within(log).findByText('GET /healthz 200 1ms')
    await userEvent.click(screen.getByRole('button', { name: 'Load older' }))
    await within(log).findByText('older line')
    expect(seen.some((url) => url.searchParams.get('before') === supportedOnly.older_before)).toBe(true)
    const messages = within(log).getAllByText(/./, { selector: 'span:last-child' }).map((el) => el.textContent)
    expect(messages[0]).toBe('older line')
    expect(screen.queryByRole('button', { name: 'Load older' })).not.toBeInTheDocument()
  })

  it('container select changes the query and disables unsupported containers', async () => {
    const seen = serveHistory(() => LOGS_HISTORY_PAYMENTS)
    renderApp(route)
    await screen.findByRole('log')
    const select = screen.getByLabelText('Container')
    const firelens = within(select).getByRole('option', { name: 'log-router' })
    expect(firelens).toBeDisabled()
    expect(firelens).toHaveAttribute('title', 'awsfirelens: Harbormaster cannot show these logs')
    await userEvent.selectOptions(select, 'app')
    await waitFor(() => expect(seen.some((url) => url.searchParams.get('container') === 'app')).toBe(true))
  })

  it('shows the driver banner for a FireLens container', async () => {
    serveHistory(() => LOGS_HISTORY_PAYMENTS)
    renderApp(route)
    expect(await screen.findByText('log-router logs go to the awsfirelens driver. Harbormaster cannot show those logs.')).toBeInTheDocument()
    expect(screen.getByRole('log')).toBeInTheDocument()
  })

  it('shows only banners when no container is supported', async () => {
    serveHistory(() => ({
      ...LOGS_HISTORY_PAYMENTS,
      containers: [LOGS_HISTORY_PAYMENTS.containers[2]],
      lines: [],
    }))
    renderApp(route)
    await screen.findByText(/log-router logs go to the awsfirelens driver/)
    expect(screen.queryByRole('log')).not.toBeInTheDocument()
    expect(MockSocket.instances).toHaveLength(0)
  })

  it('idle end shows Resume', async () => {
    serveHistory(() => supportedOnly)
    renderApp(route)
    await screen.findByRole('log')
    await waitFor(() => expect(MockSocket.instances).toHaveLength(1))
    const socket = MockSocket.instances[0]
    act(() => {
      socket.readyState = 1
      socket.onopen?.()
      socket.onmessage?.({ data: JSON.stringify({ type: 'end', reason: 'idle', message: 'idle' }) })
      socket.onclose?.({ code: 4408, reason: 'idle' })
    })
    expect(await screen.findByText('Live tail stopped after 30 minutes idle.')).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'Resume' }))
    expect(MockSocket.instances).toHaveLength(2)
  })

  it('Paused closes the socket and Live reconnects', async () => {
    serveHistory(() => supportedOnly)
    renderApp(route)
    await screen.findByRole('log')
    await waitFor(() => expect(MockSocket.instances).toHaveLength(1))
    await userEvent.click(screen.getByRole('button', { name: 'Paused' }))
    expect(MockSocket.instances[0].readyState).toBe(3)
    expect(screen.getByText('CloudWatch Logs', { exact: false })).toHaveTextContent('paused')
    await userEvent.click(screen.getByRole('button', { name: 'Live' }))
    expect(MockSocket.instances).toHaveLength(2)
  })

  it('tail limit shows the server message', async () => {
    serveHistory(() => supportedOnly)
    renderApp(route)
    await screen.findByRole('log')
    await waitFor(() => expect(MockSocket.instances).toHaveLength(1))
    act(() => MockSocket.instances[0].onclose?.({ code: 4409, reason: 'You already have 2 live tails open.' }))
    expect(await screen.findByText('You already have 2 live tails open.')).toBeInTheDocument()
  })

  it('scrolling up pauses following', async () => {
    serveHistory(() => supportedOnly)
    renderApp(route)
    const log = await screen.findByRole('log')
    Object.defineProperty(log, 'scrollHeight', { configurable: true, value: 1000 })
    Object.defineProperty(log, 'clientHeight', { configurable: true, value: 500 })
    log.scrollTop = 100
    fireEvent.scroll(log)
    await waitFor(() => expect(screen.getByRole('button', { name: 'Paused' })).toHaveAttribute('aria-pressed', 'true'))
  })

  it('csrf_invalid close tells the user to reload', async () => {
    serveHistory(() => supportedOnly)
    renderApp(route)
    await screen.findByRole('log')
    await waitFor(() => expect(MockSocket.instances).toHaveLength(1))
    act(() => MockSocket.instances[0].onclose?.({ code: 4401, reason: 'csrf_invalid' }))
    expect(await screen.findByText('Your session changed. Reload the page to follow logs again.')).toBeInTheDocument()
    expect(screen.queryByText(/live tail$/)).not.toBeInTheDocument()
  })

  it('other_group container gets the log group wording', async () => {
    serveHistory(() => LOGS_HISTORY_PAYMENTS)
    renderApp(route)
    expect(await screen.findByText('datadog-agent logs go to a different log group. Harbormaster cannot show those logs.')).toBeInTheDocument()
  })

  it('only user activity sends keepalive, not auto-scroll', async () => {
    serveHistory(() => supportedOnly)
    renderApp(route)
    const log = await screen.findByRole('log')
    await waitFor(() => expect(MockSocket.instances).toHaveLength(1))
    const socket = MockSocket.instances[0]
    const keepalives = () => socket.sent.filter((frame) => JSON.parse(frame).type === 'keepalive').length
    act(() => {
      socket.readyState = 1
      socket.onopen?.()
      socket.onmessage?.({ data: JSON.stringify({ type: 'ready', group: 'g', prefixes: [] }) })
      socket.onmessage?.({
        data: JSON.stringify({ type: 'lines', items: [{ t: '2026-10-31T10:00:00Z', container: 'app', task_id: 'a1b2c3d4e5', message: 'tail line' }] }),
      })
    })
    await screen.findByText('tail line')
    fireEvent.scroll(log)
    expect(keepalives()).toBe(0)
    fireEvent.wheel(log)
    expect(keepalives()).toBe(1)
  })

  it.each([
    ['error', 'The live tail stopped because AWS returned an error.'],
    ['closed', 'The live tail was closed.'],
  ])('end frame %s shows its message with Resume', async (reason, message) => {
    serveHistory(() => supportedOnly)
    renderApp(route)
    await screen.findByRole('log')
    await waitFor(() => expect(MockSocket.instances).toHaveLength(1))
    const socket = MockSocket.instances[0]
    act(() => {
      socket.onmessage?.({ data: JSON.stringify({ type: 'end', reason, message }) })
      socket.onclose?.({ code: 1000, reason: '' })
    })
    expect(await screen.findByText(message)).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'Resume' }))
    expect(MockSocket.instances).toHaveLength(2)
  })
})
