import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { describe, expect, it } from 'vitest'
import { ALL_TASKS, FIXTURE_NOW, OPERATIONS_FOR, SERVICE_PAYMENTS, tasksList } from '../../test/fixtures'
import { server } from '../../test/msw'
import { renderApp } from '../../test/renderApp'

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
    const table = await screen.findByRole('table')
    expect(within(table).getAllByText('Running').length).toBeGreaterThan(0)
    expect(within(table).queryByRole('button', { name: 'Stop' })).not.toBeInTheDocument()
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
    const table = await screen.findByRole('table')
    expect(within(table).getAllByText('0.25 vCPU').length).toBeGreaterThan(0)
    expect(within(table).getAllByText('512 MiB').length).toBeGreaterThan(0)
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
