import { act, cleanup, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { ExecTargets } from '../../../api/exec'
import type { TaskDTO } from '../../../api/types'
import { ALL_TASKS, SERVICE_PAYMENTS, tasksList } from '../../../test/fixtures'
import { MockWebSocket } from '../../../test/mockWebSocket'
import { server } from '../../../test/msw'
import { installPhoneMedia, installResizeObserver, installVisualViewport } from '../../../test/phone'
import { renderApp } from '../../../test/renderApp'

const xterm = vi.hoisted(() => ({ fits: 0 }))

vi.mock('@xterm/xterm', () => ({
  Terminal: class {
    cols = 100
    rows = 30
    loadAddon() {}
    open() {}
    write() {}
    onData() {
      return { dispose() {} }
    }
    onResize() {
      return { dispose() {} }
    }
    dispose() {}
  },
}))
vi.mock('@xterm/addon-fit', () => ({
  FitAddon: class {
    fit() {
      xterm.fits++
    }
  },
}))
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
  containers: [
    { name: 'payments-api', exec_agent: 'RUNNING' },
    { name: 'sidecar', exec_agent: 'RUNNING' },
  ],
  commands: ['/bin/sh', '/bin/bash'],
  can_terminal: true,
}

function seed(tasks = [runningTask()]) {
  server.use(
    http.get('/api/services/:id', () => HttpResponse.json({ ...SERVICE_PAYMENTS, enable_execute_command: true })),
    http.get('/api/services/:id/tasks', () => HttpResponse.json(tasksList(tasks))),
    http.get('/api/tasks/:id/exec-targets', () => HttpResponse.json(TARGETS)),
  )
}

const keysRow = () => screen.getByRole('toolbar', { name: 'Terminal keys' })

async function openTarget() {
  await userEvent.click(await screen.findByRole('button', { name: /^Target:/ }))
  const sheet = await screen.findByRole('dialog', { name: 'Terminal target' })
  await waitFor(() => expect(within(sheet).getByLabelText('Container')).toHaveValue('payments-api'))
  return sheet
}

/** Opens the terminal, connects through the target sheet and has the server report the session as connected. */
async function connectedSession() {
  seed()
  renderApp(`/services/${SERVICE_PAYMENTS.id}/terminal`)
  const sheet = await openTarget()
  await waitFor(() => expect(within(sheet).getByRole('button', { name: 'Connect' })).toBeEnabled())
  await userEvent.click(within(sheet).getByRole('button', { name: 'Connect' }))
  const socket = MockWebSocket.last
  await waitFor(() => expect(socket.sent).toHaveLength(1))
  act(() => socket.serverText({ type: 'status', state: 'connected', session_id: 's-1' }))
  return socket
}

const decode = (frame: string | Uint8Array | ArrayBuffer): string => (typeof frame === 'string' ? frame : new TextDecoder().decode(frame))

describe('TerminalTab on a phone', () => {
  const restores: (() => void)[] = []
  beforeEach(() => {
    restores.push(installPhoneMedia(true), installResizeObserver())
    MockWebSocket.install()
    xterm.fits = 0
  })
  afterEach(() => {
    cleanup()
    restores.splice(0).forEach((restore) => restore())
    document.documentElement.removeAttribute('data-hm-keys')
  })

  it('keys row shows while connected and sets data-hm-keys', async () => {
    seed()
    renderApp(`/services/${SERVICE_PAYMENTS.id}/terminal`)
    await screen.findByRole('button', { name: /^Target:/ })
    expect(screen.queryByRole('toolbar', { name: 'Terminal keys' })).not.toBeInTheDocument()
    expect(document.documentElement).not.toHaveAttribute('data-hm-keys')
    cleanup()

    await connectedSession()
    expect(keysRow()).toBeInTheDocument()
    expect(document.documentElement).toHaveAttribute('data-hm-keys')
  })

  it('keys send the escape sequences', async () => {
    const socket = await connectedSession()
    const expected: [string, string][] = [
      ['Send Escape', '\x1b'],
      ['Send Tab', '\t'],
      ['Send Control-C', '\x03'],
      ['Send Up', '\x1b[A'],
      ['Send Down', '\x1b[B'],
      ['Send Left', '\x1b[D'],
      ['Send Right', '\x1b[C'],
    ]
    for (const [name, bytes] of expected) {
      const before = socket.sent.length
      await userEvent.click(within(keysRow()).getByRole('button', { name }))
      await waitFor(() => expect(socket.sent).toHaveLength(before + 1))
      expect(decode(socket.sent[before])).toBe(bytes)
    }
  })

  it('target sheet changes task/container/command and Connect', async () => {
    seed([runningTask(), runningTask({ id: 'bbbbbbbbbbbb1111', short_id: 'bbbbbbbbbbbb1111', private_ip: '10.20.2.7' })])
    renderApp(`/services/${SERVICE_PAYMENTS.id}/terminal`)
    expect(await screen.findByRole('button', { name: /^Target:/ })).toHaveTextContent('6faba45f9978')
    const sheet = await openTarget()
    await userEvent.selectOptions(within(sheet).getByLabelText('Task'), 'bbbbbbbbbbbb1111')
    await waitFor(() => expect(within(sheet).getByLabelText('Container')).toHaveValue('payments-api'))
    await userEvent.selectOptions(within(sheet).getByLabelText('Container'), 'sidecar')
    await userEvent.selectOptions(within(sheet).getByLabelText('Command'), '/bin/bash')
    await userEvent.click(within(sheet).getByRole('button', { name: 'Connect' }))
    const socket = MockWebSocket.last
    await waitFor(() => expect(socket.sent).toHaveLength(1))
    expect(socket.url).toContain('bbbbbbbbbbbb1111')
    expect(socket.url).toContain('sidecar')
    expect(decodeURIComponent(socket.url)).toContain('/bin/bash')
    await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Terminal target' })).not.toBeInTheDocument())
    expect(screen.getByRole('button', { name: /^Target:/ })).toHaveTextContent('sidecar')
  })

  it('session sheet carries the session facts behind the info button', async () => {
    seed()
    renderApp(`/services/${SERVICE_PAYMENTS.id}/terminal`)
    await screen.findByRole('button', { name: /^Target:/ })
    expect(screen.queryByRole('region', { name: 'Session' })).not.toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'Session details' }))
    const sheet = await screen.findByRole('dialog', { name: 'Session' })
    expect(sheet).toHaveTextContent('Idle timeout20 minutes')
    expect(sheet).toHaveTextContent('Keystrokes and output are not recorded.')
  })

  it('disconnect removes data-hm-keys', async () => {
    await connectedSession()
    await userEvent.click(screen.getByRole('button', { name: 'Disconnect' }))
    await waitFor(() => expect(document.documentElement).not.toHaveAttribute('data-hm-keys'))
    expect(screen.queryByRole('toolbar', { name: 'Terminal keys' })).not.toBeInTheDocument()
  })

  it('unmount removes data-hm-keys', async () => {
    await connectedSession()
    expect(document.documentElement).toHaveAttribute('data-hm-keys')
    cleanup()
    expect(document.documentElement).not.toHaveAttribute('data-hm-keys')
  })

  it('refits the terminal when the visual viewport changes', async () => {
    const viewport = installVisualViewport({ height: 800, offsetTop: 0 })
    restores.push(viewport.restore)
    seed()
    renderApp(`/services/${SERVICE_PAYMENTS.id}/terminal`)
    await screen.findByRole('button', { name: /^Target:/ })
    const before = xterm.fits
    act(() => viewport.set({ height: 480 }))
    expect(xterm.fits).toBeGreaterThan(before)
  })
})
