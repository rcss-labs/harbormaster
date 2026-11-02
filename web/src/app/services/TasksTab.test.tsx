import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { describe, expect, it } from 'vitest'
import type { TaskDTO } from '../../api/types'
import { ALL_TASKS, FIXTURE_NOW, OPERATIONS_FOR, SERVICE_PAYMENTS, tasksList } from '../../test/fixtures'
import { server } from '../../test/msw'
import { renderApp } from '../../test/renderApp'

/** The table renders as soon as the page mounts, before the tasks request resolves; wait for a data row. */
async function findTaskTable() {
  const table = await screen.findByRole('table')
  await waitFor(() => expect(within(table).getAllByRole('row').length).toBeGreaterThan(1))
  return table
}

describe('TasksTab', () => {
  it('hides Stop when can.operate is false', async () => {
    server.use(
      http.get('/api/services/:id/tasks', ({ params }) =>
        HttpResponse.json(
          tasksList(
            ALL_TASKS.filter((task) => task.service?.id === params.id).map((task) => ({ ...task, can: { ...task.can, operate: false } })),
          ),
        ),
      ),
    )
    renderApp(`/services/${SERVICE_PAYMENTS.id}/tasks`)
    const table = await findTaskTable()
    expect(within(table).getAllByText('Running').length).toBeGreaterThan(0)
    expect(within(table).queryByRole('button', { name: 'Stop' })).not.toBeInTheDocument()
  })

  it('enables Stop on running rows only', async () => {
    renderApp(`/services/${SERVICE_PAYMENTS.id}/tasks`)
    const table = await findTaskTable()
    const runningCount = ALL_TASKS.filter((task) => task.service?.id === SERVICE_PAYMENTS.id && task.status === 'RUNNING').length
    const stopButtons = within(table).getAllByRole('button', { name: 'Stop' })
    expect(stopButtons).toHaveLength(runningCount)
    stopButtons.forEach((button) => expect(button).toBeEnabled())
  })

  it('Stop opens the dialog with the 12-character short id and the service desired count', async () => {
    const longShortId = 'abcdef0123456789abcdef0123456789'
    server.use(
      http.get('/api/services/:id/tasks', ({ params }) =>
        HttpResponse.json(
          tasksList(
            ALL_TASKS.filter((task) => task.service?.id === params.id).map((task) => ({ ...task, short_id: longShortId })),
          ),
        ),
      ),
      http.get('/api/services/:id/desired-count', () => HttpResponse.json({ desired: 3, running: 3, max: 50, auto_scaling: null })),
    )
    renderApp(`/services/${SERVICE_PAYMENTS.id}/tasks`)
    const table = await findTaskTable()
    await userEvent.setup().click(within(table).getAllByRole('button', { name: 'Stop' })[0])
    expect(await screen.findByRole('heading', { name: 'Stop task abcdef012345?' })).toBeInTheDocument()
    expect(screen.getByText(`${SERVICE_PAYMENTS.name} will start a replacement to keep 3 running.`)).toBeInTheDocument()
  })

  it('shows fractional vCPU exactly and memory in MiB', async () => {
    server.use(
      http.get('/api/services/:id/tasks', ({ params }) =>
        HttpResponse.json(
          tasksList(ALL_TASKS.filter((task) => task.service?.id === params.id).map((task) => ({ ...task, cpu: '256', memory: '512' }))),
        ),
      ),
    )
    renderApp(`/services/${SERVICE_PAYMENTS.id}/tasks`)
    const table = await findTaskTable()
    expect(within(table).getAllByText('0.25 vCPU').length).toBeGreaterThan(0)
    expect(within(table).getAllByText('512 MiB').length).toBeGreaterThan(0)
  })

  it('asks for every task of the service, not only the default problems view', async () => {
    const filters: (string | null)[] = []
    server.use(
      http.get('/api/services/:id/tasks', ({ params, request }) => {
        filters.push(new URL(request.url).searchParams.get('filter'))
        return HttpResponse.json(tasksList(ALL_TASKS.filter((task) => task.service?.id === params.id)))
      }),
    )
    renderApp(`/services/${SERVICE_PAYMENTS.id}/tasks`)
    await screen.findByRole('table')
    expect(filters.length).toBeGreaterThan(0)
    expect(new Set(filters)).toEqual(new Set(['all']))
  })

  it('Load more requests the next cursor', async () => {
    const cursors: (string | null)[] = []
    server.use(
      http.get('/api/services/:id/tasks', ({ params, request }) => {
        const cursor = new URL(request.url).searchParams.get('cursor')
        cursors.push(cursor)
        const serviceTasks = ALL_TASKS.filter((task) => task.service?.id === params.id)
        const page = cursor ? tasksList([]) : tasksList(serviceTasks)
        return HttpResponse.json({ ...page, next: cursor ? null : 'page-2' })
      }),
    )
    renderApp(`/services/${SERVICE_PAYMENTS.id}/tasks`)
    await userEvent.setup().click(await screen.findByRole('button', { name: 'Load more' }))
    await waitFor(() => expect(cursors).toContain('page-2'))
    expect(within(screen.getByRole('table')).getAllByText('Running').length).toBeGreaterThan(0)
  })
})

describe('TasksTab hidden-stopped note', () => {
  it('shows the hidden-tasks note', async () => {
    renderApp(`/services/${SERVICE_PAYMENTS.id}/tasks`)
    expect(await screen.findByText('Stopped tasks older than 10 min are hidden.')).toBeInTheDocument()
  })
})

describe('TasksTab Terminal link', () => {
  const serviceTasks = () => ALL_TASKS.filter((task) => task.service?.id === SERVICE_PAYMENTS.id)
  const terminalHref = (taskId: string) => `/services/${SERVICE_PAYMENTS.id}/terminal?task=${encodeURIComponent(taskId)}`

  function withServiceTasks(map: (task: TaskDTO) => TaskDTO) {
    server.use(http.get('/api/services/:id/tasks', () => HttpResponse.json(tasksList(serviceTasks().map(map)))))
  }

  it('shows Terminal on running rows when exec is on and user has Terminal', async () => {
    renderApp(`/services/${SERVICE_PAYMENTS.id}/tasks`)
    const table = await findTaskTable()
    const running = serviceTasks().filter((task) => task.status === 'RUNNING')
    const links = within(table).getAllByRole('link', { name: 'Terminal' })
    expect(links.map((link) => link.getAttribute('href')).sort()).toEqual(running.map((task) => terminalHref(task.id)).sort())
  })

  it('hides Terminal when exec is off', async () => {
    withServiceTasks((task) => ({ ...task, exec_enabled: false, can: { ...task.can, terminal: true } }))
    renderApp(`/services/${SERVICE_PAYMENTS.id}/tasks`)
    const table = await findTaskTable()
    expect(within(table).getAllByRole('button', { name: 'Stop' }).length).toBeGreaterThan(0)
    expect(within(table).queryByRole('link', { name: 'Terminal' })).not.toBeInTheDocument()
  })

  it('hides Terminal without Terminal permission', async () => {
    withServiceTasks((task) => ({ ...task, exec_enabled: true, can: { ...task.can, terminal: false } }))
    renderApp(`/services/${SERVICE_PAYMENTS.id}/tasks`)
    const table = await findTaskTable()
    expect(within(table).getAllByRole('button', { name: 'Stop' }).length).toBeGreaterThan(0)
    expect(within(table).queryByRole('link', { name: 'Terminal' })).not.toBeInTheDocument()
  })

  it('no Terminal on stopped rows', async () => {
    withServiceTasks((task) => ({ ...task, status: 'STOPPED', exit_code: 0, exec_enabled: true, can: { ...task.can, terminal: true } }))
    renderApp(`/services/${SERVICE_PAYMENTS.id}/tasks`)
    const table = await findTaskTable()
    expect(within(table).getAllByText('Stopped').length).toBeGreaterThan(0)
    expect(within(table).queryByRole('link', { name: 'Terminal' })).not.toBeInTheDocument()
  })
})

describe('TasksTab segments', () => {
  it('filters the loaded tasks to Running or Stopped and counts each', async () => {
    const own = ALL_TASKS.filter((task) => task.service?.id === SERVICE_PAYMENTS.id && task.status === 'RUNNING')
    const stopped = { ...own[0], id: 'stopped-1', short_id: 'dead00000001', status: 'STOPPED', exit_code: 137 }
    server.use(http.get('/api/services/:id/tasks', () => HttpResponse.json(tasksList([...own, stopped]))))
    renderApp(`/services/${SERVICE_PAYMENTS.id}/tasks`)
    const table = await findTaskTable()
    const segments = screen.getByRole('radiogroup', { name: 'Task state' })
    expect(within(segments).getByRole('radio', { name: `Running ${own.length}` })).toBeInTheDocument()
    expect(within(segments).getByRole('radio', { name: 'Stopped 1' })).toBeInTheDocument()
    expect(within(segments).getByRole('radio', { name: 'All' + ` ${own.length + 1}` })).toBeChecked()

    await userEvent.setup().click(within(segments).getByRole('radio', { name: 'Stopped 1' }))
    expect(within(table).queryByText('Running')).not.toBeInTheDocument()
    expect(within(table).getByText('dead00000001')).toBeInTheDocument()

    await userEvent.setup().click(within(segments).getByRole('radio', { name: `Running ${own.length}` }))
    expect(within(table).queryByText('dead00000001')).not.toBeInTheDocument()
    expect(within(table).getAllByText('Running').length).toBeGreaterThan(0)
  })
})

describe('OperationsTab', () => {
  it('unchecking the toggle requests include_ecs=false', async () => {
    const requested: string[] = []
    server.use(
      http.get('/api/services/:id/operations', ({ params, request }) => {
        requested.push(request.url)
        return HttpResponse.json({ items: OPERATIONS_FOR(params.id as string), next: null, polled_at: FIXTURE_NOW })
      }),
    )
    renderApp(`/services/${SERVICE_PAYMENTS.id}/operations`)
    const toggle = await screen.findByRole('checkbox', { name: 'Show ECS service events' })
    expect(toggle).toBeChecked()
    await userEvent.setup().click(toggle)
    await waitFor(() => expect(requested.some((url) => url.includes('include_ecs=false'))).toBe(true))
  })
})
