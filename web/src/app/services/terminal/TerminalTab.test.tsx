import { act, cleanup, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { ExecTargets } from '../../../api/exec'
import type { ServiceDTO, TaskDTO } from '../../../api/types'
import { ALL_TASKS, ME_ADMIN, SERVICE_PAYMENTS, tasksList } from '../../../test/fixtures'
import { MockWebSocket } from '../../../test/mockWebSocket'
import { server } from '../../../test/msw'
import { renderApp } from '../../../test/renderApp'

const xterm = vi.hoisted(() => ({
  written: [] as string[],
  disposed: 0,
  dataHandlers: [] as ((d: string) => void)[],
}))

vi.mock('@xterm/xterm', () => ({
  Terminal: class {
    cols = 100
    rows = 30
    loadAddon() {}
    open() {}
    write(data: string | Uint8Array) {
      xterm.written.push(typeof data === 'string' ? data : new TextDecoder().decode(data))
    }
    onData(handler: (d: string) => void) {
      xterm.dataHandlers.push(handler)
      return { dispose() {} }
    }
    onResize() {
      return { dispose() {} }
    }
    dispose() {
      xterm.disposed++
    }
  },
}))
vi.mock('@xterm/addon-fit', () => ({ FitAddon: class { fit() {} } }))
vi.mock('@xterm/xterm/css/xterm.css', () => ({}))

const runningTask = (over: Partial<TaskDTO> = {}): TaskDTO => ({
  ...ALL_TASKS.find((t) => t.service?.id === SERVICE_PAYMENTS.id)!,
  id: '6faba45f99785a3c1d2e',
  short_id: '6faba45f99785a3c1d2e',
  status: 'RUNNING',
  private_ip: '10.20.1.149',
  az: 'us-east-1a',
  ...over,
})

const TARGETS: ExecTargets = {
  task_id: 'x',
  exec_enabled: true,
  containers: [{ name: 'payments-api', exec_agent: 'RUNNING' }],
  commands: ['/bin/sh', '/bin/bash'],
  can_terminal: true,
}

function seed({ service = {}, tasks = [runningTask()], targets = TARGETS }: { service?: Partial<ServiceDTO>; tasks?: TaskDTO[]; targets?: ExecTargets } = {}) {
  server.use(
    http.get('/api/services/:id', () => HttpResponse.json({ ...SERVICE_PAYMENTS, enable_execute_command: true, ...service })),
    http.get('/api/services/:id/tasks', () => HttpResponse.json(tasksList(tasks))),
    http.get('/api/tasks/:id/exec-targets', () => HttpResponse.json(targets)),
  )
}

const open = (search = '') => renderApp(`/services/${SERVICE_PAYMENTS.id}/terminal${search}`)
const output = () => xterm.written.join('')

describe('TerminalTab', () => {
  beforeEach(() => {
    MockWebSocket.install()
    xterm.written = []
    xterm.disposed = 0
    xterm.dataHandlers = []
  })

  it('shows a session panel with the idle and duration limits and says keystrokes are not recorded', async () => {
    seed()
    open()
    const panel = await screen.findByRole('region', { name: 'Session' })
    expect(panel).toHaveTextContent('ECS Exec')
    expect(panel).toHaveTextContent('Idle timeout20 minutes')
    expect(panel).toHaveTextContent('Max duration4 hours')
    expect(panel).toHaveTextContent('Keystrokes and output are not recorded.')
  })

  it('the terminal sits in a panel that fills the page height, beside the session panel', async () => {
    seed()
    open()
    const host = await screen.findByTestId('terminal-host')
    expect(host.closest('.hm-fill-col')).not.toBeNull()
    expect(host.closest('.hm-fill')).toHaveClass('hm-with-side')
    expect(screen.getByRole('region', { name: 'Session' })).toBeInTheDocument()
  })

  it('shows the lock panel without Terminal permission', async () => {
    seed({ service: { can: { ...SERVICE_PAYMENTS.can, terminal: false } } })
    open()
    expect(await screen.findByText(/to use this/)).toBeInTheDocument()
    expect(screen.getByText('Terminal', { selector: 'strong' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Connect' })).not.toBeInTheDocument()
  })

  it('shows the exec-off banner with the Terraform setting', async () => {
    seed({ service: { enable_execute_command: false } })
    open()
    expect(await screen.findByText('ECS Exec is off for payments-api.')).toBeInTheDocument()
    expect(screen.getByText('enable_execute_command = true')).toBeInTheDocument()
  })

  it('shows empty state without running tasks', async () => {
    seed({ tasks: [runningTask({ status: 'STOPPED' })] })
    open()
    expect(await screen.findByText('No running tasks to connect to.')).toBeInTheDocument()
  })

  it('renders task, container and command selectors with defaults', async () => {
    seed()
    open()
    expect(await screen.findByRole('option', { name: '6faba45f9978 · 10.20.1.149 · us-east-1a' })).toBeInTheDocument()
    expect(screen.getByLabelText('Task')).toHaveValue('6faba45f99785a3c1d2e')
    await waitFor(() => expect(screen.getByLabelText('Container')).toHaveValue('payments-api'))
    expect(screen.getByLabelText('Command')).toHaveValue('/bin/sh')
    expect(screen.getByRole('button', { name: 'Connect' })).toBeEnabled()
    expect(output()).toContain('Pick a task and a container, then Connect.')
  })

  it('preselects the task from ?task= search param', async () => {
    seed({ tasks: [runningTask(), runningTask({ id: 'bbbbbbbbbbbb1111', short_id: 'bbbbbbbbbbbb1111' })] })
    open('?task=bbbbbbbbbbbb1111')
    await waitFor(() => expect(screen.getByLabelText('Task')).toHaveValue('bbbbbbbbbbbb1111'))
  })

  it('disables containers whose agent is not RUNNING and the Connect button', async () => {
    seed({ targets: { ...TARGETS, containers: [{ name: 'payments-api', exec_agent: 'PENDING' }] } })
    open()
    const option = await screen.findByRole('option', { name: 'payments-api · agent pending' })
    expect(option).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Connect' })).toBeDisabled()
  })

  it('keeps Connect disabled while the CSRF token is unknown', async () => {
    seed()
    server.use(http.get('/api/me', () => HttpResponse.json({ ...ME_ADMIN, csrf_token: '' })))
    open()
    const button = await screen.findByRole('button', { name: 'Connect' })
    await waitFor(() => expect(screen.getByLabelText('Container')).toHaveValue('payments-api'))
    expect(button).toBeDisabled()
  })

  async function connect() {
    seed()
    open()
    const button = await screen.findByRole('button', { name: 'Connect' })
    await waitFor(() => expect(screen.getByLabelText('Container')).toHaveValue('payments-api'))
    await waitFor(() => expect(button).toBeEnabled())
    await userEvent.click(button)
    const socket = MockWebSocket.last
    await waitFor(() => expect(socket.sent).toHaveLength(1))
    return socket
  }

  it('Connect sends start with the terminal size and locks the selectors', async () => {
    const socket = await connect()
    expect(JSON.parse(socket.sent[0] as string)).toMatchObject({ type: 'start', cols: 100, rows: 30 })
    act(() => socket.serverText({ type: 'status', state: 'connected', session_id: 's-1' }))
    expect(await screen.findByRole('button', { name: 'Disconnect' })).toBeInTheDocument()
    expect(screen.getByLabelText('Task')).toBeDisabled()
    expect(screen.getByLabelText('Container')).toBeDisabled()
    expect(screen.getByLabelText('Command')).toBeDisabled()
  })

  it('writes the Starting session line with the session id on connected', async () => {
    const socket = await connect()
    act(() => socket.serverText({ type: 'status', state: 'connected', session_id: 's-1' }))
    expect(output()).toContain('Starting session with SessionId: s-1')
  })

  it('writes the Exiting session line on closed and re-enables selectors', async () => {
    const socket = await connect()
    act(() => {
      socket.serverText({ type: 'status', state: 'connected', session_id: 's-1' })
      socket.serverText({ type: 'status', state: 'closed', session_id: 's-1', reason: 'idle_timeout', duration_ms: 5 })
      socket.serverClose(4408)
    })
    expect(output()).not.toContain('Connection closed')
    expect(output()).toContain('Exiting session with sessionId: s-1.')
    expect(output()).toContain('Disconnected after 20 minutes without input.')
    await waitFor(() => expect(screen.getByLabelText('Task')).toBeEnabled())
    expect(screen.getByRole('button', { name: 'Connect' })).toBeInTheDocument()
  })

  it('writes the server error message on error', async () => {
    const socket = await connect()
    act(() => {
      socket.serverText({ type: 'error', code: 'session_limit', message: 'You already have 3 open terminal sessions.' })
      socket.serverClose(4409)
    })
    expect(output()).toContain('You already have 3 open terminal sessions.')
    expect(output()).toContain('Close another terminal to open a new one.')
  })

  it('disconnects and disposes on unmount', async () => {
    const socket = await connect()
    cleanup()
    expect(xterm.disposed).toBeGreaterThan(0)
    expect(socket.closedWith).toBe(1000)
  })
})
