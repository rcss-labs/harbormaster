import { fireEvent, screen, waitFor, within } from '@testing-library/react'
import { http, HttpResponse } from 'msw'
import { describe, expect, it } from 'vitest'
import type { TaskDTO } from '../../api/types'
import { ALL_TASKS, TASKS, tasksList } from '../../test/fixtures'
import { server } from '../../test/msw'
import { renderApp } from '../../test/renderApp'

describe('Tasks', () => {
  it('seg buttons show counts and set ?filter=', async () => {
    renderApp('/tasks?keep=1')
    expect(await screen.findByRole('button', { name: `Problems first ${TASKS.counts.problems}` })).toHaveAttribute('aria-pressed', 'true')
    expect(screen.getByRole('button', { name: `Running ${TASKS.counts.running}` })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: `Stopped ${TASKS.counts.stopped}` })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: `All ${TASKS.counts.all}` })).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: `Stopped ${TASKS.counts.stopped}` }))
    await waitFor(() => expect(screen.getByTestId('search')).toHaveTextContent('filter=stopped'))
    expect(screen.getByTestId('search')).toHaveTextContent('keep=1')
  })

  it('error-exit row has bad stripe and "Stopped (exit 137)"', async () => {
    renderApp('/tasks')
    const [pill] = await screen.findAllByText('Stopped (exit 137)')
    const row = pill.closest('tr')!
    expect(row).toHaveClass('stripe', 'bad')
  })

  it('standalone row shows group and started_by', async () => {
    renderApp('/tasks?filter=running')
    const row = (await screen.findByText('family:release-build')).closest('tr')!
    expect(within(row).getByText('Standalone · started by events-rule/nightly-release')).toBeInTheDocument()
  })
})

describe('Tasks Terminal link', () => {
  const serviceRunning = ALL_TASKS.find((task) => task.service && task.status === 'RUNNING')!
  const standaloneRunning = ALL_TASKS.find((task) => !task.service && task.status === 'RUNNING')!

  function withTasks(items: TaskDTO[]) {
    server.use(http.get('/api/tasks', () => HttpResponse.json(tasksList(items))))
  }

  it('shows Terminal on a running service row when exec is on and user has Terminal', async () => {
    withTasks([{ ...serviceRunning, exec_enabled: true, can: { ...serviceRunning.can, terminal: true } }])
    renderApp('/tasks?filter=running')
    const link = await screen.findByRole('link', { name: 'Terminal' })
    expect(link).toHaveAttribute('href', `/services/${serviceRunning.service!.id}/terminal?task=${encodeURIComponent(serviceRunning.id)}`)
  })

  it('hides Terminal when exec is off', async () => {
    withTasks([{ ...serviceRunning, exec_enabled: false, can: { ...serviceRunning.can, terminal: true } }])
    renderApp('/tasks?filter=running')
    const row = (await screen.findByText(serviceRunning.service!.name)).closest('tr')!
    expect(within(row).getByRole('button', { name: 'Stop' })).toBeInTheDocument()
    expect(within(row).queryByRole('link', { name: 'Terminal' })).not.toBeInTheDocument()
  })

  it('hides Terminal without Terminal permission', async () => {
    withTasks([{ ...serviceRunning, exec_enabled: true, can: { ...serviceRunning.can, terminal: false } }])
    renderApp('/tasks?filter=running')
    const row = (await screen.findByText(serviceRunning.service!.name)).closest('tr')!
    expect(within(row).getByRole('button', { name: 'Stop' })).toBeInTheDocument()
    expect(within(row).queryByRole('link', { name: 'Terminal' })).not.toBeInTheDocument()
  })

  it('no Terminal on stopped rows', async () => {
    withTasks([{ ...serviceRunning, status: 'STOPPED', exit_code: 0, exec_enabled: true, can: { ...serviceRunning.can, terminal: true } }])
    renderApp('/tasks?filter=stopped')
    const row = (await screen.findByText(serviceRunning.service!.name)).closest('tr')!
    expect(within(row).queryByRole('link', { name: 'Terminal' })).not.toBeInTheDocument()
    expect(within(row).queryByRole('button', { name: 'Stop' })).not.toBeInTheDocument()
  })

  it('shows Terminal on a running standalone row, linking to the standalone terminal page', async () => {
    withTasks([{ ...standaloneRunning, exec_enabled: true, can: { ...standaloneRunning.can, terminal: true } }])
    renderApp('/tasks?filter=running')
    const row = (await screen.findByText(`family:${standaloneRunning.family}`)).closest('tr')!
    expect(within(row).getByRole('link', { name: 'Terminal' })).toHaveAttribute(
      'href',
      `/tasks/${encodeURIComponent(standaloneRunning.short_id)}/terminal`,
    )
  })

  it('hides Terminal on a standalone row when the task was started without exec', async () => {
    withTasks([{ ...standaloneRunning, exec_enabled: false, can: { ...standaloneRunning.can, terminal: true } }])
    renderApp('/tasks?filter=running')
    const row = (await screen.findByText(`family:${standaloneRunning.family}`)).closest('tr')!
    expect(within(row).getByRole('button', { name: 'Stop' })).toBeInTheDocument()
    expect(within(row).queryByRole('link', { name: 'Terminal' })).not.toBeInTheDocument()
  })
})
