import { screen, waitFor } from '@testing-library/react'
import { http, HttpResponse } from 'msw'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { ExecTargets } from '../../api/exec'
import type { TaskDTO } from '../../api/types'
import { ALL_TASKS, tasksList } from '../../test/fixtures'
import { MockWebSocket } from '../../test/mockWebSocket'
import { server } from '../../test/msw'
import { renderApp } from '../../test/renderApp'

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
vi.mock('@xterm/addon-fit', () => ({ FitAddon: class { fit() {} } }))
vi.mock('@xterm/xterm/css/xterm.css', () => ({}))

const standalone = ALL_TASKS.find((task) => !task.service && task.status === 'RUNNING')!

const TARGETS: ExecTargets = {
  task_id: standalone.id,
  exec_enabled: true,
  containers: [{ name: 'release-build', exec_agent: 'RUNNING' }],
  commands: ['/bin/sh', '/bin/bash'],
  can_terminal: true,
}

function seed(over: Partial<TaskDTO> = {}) {
  const task = { ...standalone, exec_enabled: true, can: { ...standalone.can, terminal: true }, ...over }
  server.use(
    http.get('/api/tasks', () => HttpResponse.json(tasksList([task]))),
    http.get('/api/tasks/:id/exec-targets', () => HttpResponse.json(TARGETS)),
  )
}

const open = () => renderApp(`/tasks/${encodeURIComponent(standalone.short_id)}/terminal`)

describe('Standalone task terminal page', () => {
  beforeEach(() => MockWebSocket.install())

  it('offers Connect for a running standalone task', async () => {
    seed()
    open()
    await waitFor(() => expect(screen.getByLabelText('Container')).toHaveValue('release-build'))
    expect(screen.getByRole('button', { name: 'Connect' })).toBeEnabled()
  })

  it('explains that the task was started without ECS Exec', async () => {
    seed({ exec_enabled: false })
    open()
    expect(await screen.findByText(/started without ECS Exec/)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Connect' })).not.toBeInTheDocument()
  })

  it('tells the user when the task is no longer running', async () => {
    server.use(http.get('/api/tasks', () => HttpResponse.json(tasksList([]))))
    open()
    expect(await screen.findByText('This task is not running.')).toBeInTheDocument()
  })
})
