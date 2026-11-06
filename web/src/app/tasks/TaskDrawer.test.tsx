import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { describe, expect, it, vi } from 'vitest'
import type { TaskDTO } from '../../api/types'
import { ALL_TASKS } from '../../test/fixtures'
import { server } from '../../test/msw'
import { renderWithProviders } from '../../test/setup'
import { TaskDrawer } from './TaskDrawer'

const running = ALL_TASKS.find((task) => task.service && task.status === 'RUNNING')!
const standalone = ALL_TASKS.find((task) => !task.service && task.status === 'RUNNING')!
const oomStop: TaskDTO = {
  ...running,
  status: 'STOPPED',
  exit_code: 137,
  stop_kind: 'error',
  stop_cause: '',
  stop_reason_group: 'Out of memory (exit 137)',
  stopped_reason: 'OutOfMemoryError: Container killed due to memory usage',
  stopped_at: '2026-01-01T00:00:00Z',
}

function renderDrawer(task: TaskDTO, handlers: { onClose?: () => void; onStop?: (task: TaskDTO) => void } = {}) {
  return renderWithProviders(<TaskDrawer task={task} onClose={handlers.onClose ?? (() => {})} onStop={handlers.onStop ?? (() => {})} />)
}

describe('TaskDrawer', () => {
  it('renders the friendly stop reason and the raw ECS message', async () => {
    renderDrawer(oomStop)
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText('Out of memory (exit 137)')).toBeInTheDocument()
    expect(within(dialog).getByText('OutOfMemoryError: Container killed due to memory usage')).toBeInTheDocument()
    expect(within(dialog).getByText('Stopped (exit 137)')).toBeInTheDocument()
  })

  it('shows the identity rows with the full id and ARN', async () => {
    renderDrawer(running)
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText(running.id)).toBeInTheDocument()
    expect(within(dialog).getByText(running.arn)).toBeInTheDocument()
  })

  it('standalone task shows the no-logs line and never asks for logs', async () => {
    const asked = vi.fn()
    server.use(
      http.get('/api/services/:id/logs', () => {
        asked()
        return HttpResponse.json({})
      }),
    )
    renderDrawer(standalone)
    expect(await screen.findByText('No service, so no logs here.')).toBeInTheDocument()
    expect(asked).not.toHaveBeenCalled()
  })

  it('requests the 20 newest lines of this task and links to the logs', async () => {
    let seen: URL | undefined
    server.use(
      http.get('/api/services/:id/logs', ({ request }) => {
        seen = new URL(request.url)
        return HttpResponse.json({
          group: '/ecs/x',
          region: 'eu-west-1',
          containers: [],
          lines: [{ t: '2026-01-01T00:00:05Z', container: 'app', task_id: running.short_id, message: 'POST /v1/charges 201' }],
          older_before: null,
          truncated: false,
        })
      }),
    )
    renderDrawer(running)
    expect(await screen.findByText('POST /v1/charges 201')).toBeInTheDocument()
    expect(seen?.searchParams.get('task')).toBe(running.short_id)
    expect(seen?.searchParams.get('limit')).toBe('20')
    expect(screen.getByRole('link', { name: 'Open logs' })).toHaveAttribute('href', `/services/${running.service!.id}/logs`)
  })

  it('Stop is hidden without operate and calls onStop with it', async () => {
    const onStop = vi.fn()
    const { unmount } = renderDrawer({ ...running, can: { operate: false, terminal: false } }, { onStop })
    await screen.findByRole('dialog')
    expect(screen.queryByRole('button', { name: 'Stop task' })).not.toBeInTheDocument()
    unmount()

    const operable = { ...running, can: { operate: true, terminal: false } }
    renderDrawer(operable, { onStop })
    await userEvent.click(await screen.findByRole('button', { name: 'Stop task' }))
    expect(onStop).toHaveBeenCalledWith(operable)
  })

  it('no Stop on a stopped task', async () => {
    renderDrawer({ ...oomStop, can: { operate: true, terminal: true } })
    await screen.findByRole('dialog')
    expect(screen.queryByRole('button', { name: 'Stop task' })).not.toBeInTheDocument()
  })

  it('Terminal follows the permission and ECS Exec rules', async () => {
    const first = renderDrawer({ ...running, exec_enabled: true, can: { operate: false, terminal: true } })
    expect(await screen.findByRole('link', { name: 'Terminal' })).toHaveAttribute(
      'href',
      `/services/${running.service!.id}/terminal?task=${encodeURIComponent(running.id)}`,
    )
    first.unmount()

    const second = renderDrawer({ ...running, exec_enabled: true, can: { operate: false, terminal: false } })
    await screen.findByRole('dialog')
    expect(screen.queryByText('Terminal')).not.toBeInTheDocument()
    second.unmount()

    renderDrawer({ ...running, exec_enabled: false, can: { operate: false, terminal: true } })
    const disabled = await screen.findByText('Terminal')
    expect(disabled).toHaveAttribute('aria-disabled', 'true')
    expect(disabled).toHaveAttribute('title', 'ECS Exec is off for this task')
  })

  it('lists the containers of a running task from its exec targets', async () => {
    server.use(
      http.get('/api/tasks/:id/exec-targets', () =>
        HttpResponse.json({
          task_id: running.id,
          exec_enabled: true,
          containers: [
            { name: 'app', exec_agent: 'RUNNING' },
            { name: 'otel', exec_agent: 'STOPPED' },
          ],
          commands: ['/bin/sh'],
          can_terminal: true,
        }),
      ),
    )
    renderDrawer(running)
    const table = await screen.findByRole('table', { name: 'Containers' })
    await waitFor(() => expect(within(table).getByText('otel')).toBeInTheDocument())
    expect(within(table).getByText('STOPPED')).toBeInTheDocument()
  })
})
