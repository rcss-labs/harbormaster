import { fireEvent, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import type { TaskDTO } from '../../api/types'
import { ALL_TASKS, tasksList } from '../../test/fixtures'
import { server } from '../../test/msw'
import { installPhoneMedia } from '../../test/phone'
import { renderApp } from '../../test/renderApp'

const template = ALL_TASKS.find((task) => task.service && task.status === 'RUNNING' && task.health === 'HEALTHY')!

let counter = 0
function makeTask(overrides: Partial<TaskDTO> & { serviceName?: string; clusterName?: string } = {}): TaskDTO {
  const { serviceName = 'svc-a', clusterName = 'cluster-a', ...rest } = overrides
  counter++
  const n = String(counter).padStart(4, '0')
  return {
    ...template,
    id: `phone-task-${n}`,
    short_id: `fedcba${n}0000`,
    private_ip: `10.8.0.${counter}`,
    service: { ...template.service!, id: `svc-id-${serviceName}`, name: serviceName },
    cluster: { id: `cluster-id-${clusterName}`, name: clusterName },
    exec_enabled: true,
    can: { operate: true, terminal: true },
    ...rest,
  }
}
const stoppedError = (extra: Partial<TaskDTO> & { serviceName?: string } = {}) =>
  makeTask({
    status: 'STOPPED',
    exit_code: 137,
    health: 'UNKNOWN',
    stop_code: 'EssentialContainerExited',
    stopped_reason: 'OutOfMemoryError: container killed',
    stop_reason_group: 'Out of memory (exit 137)',
    stop_kind: 'error',
    stopped_at: new Date(Date.now() - 120_000).toISOString(),
    ...extra,
  })

function serve(items: TaskDTO[]) {
  server.use(http.get('/api/tasks', () => HttpResponse.json(tasksList(items))))
}
const rows = () => within(screen.getByRole('table', { name: 'Tasks' })).getAllByRole('row')
const shortId = (task: TaskDTO) => task.short_id.slice(0, 12)

let restorePhone: () => void
beforeEach(() => {
  restorePhone = installPhoneMedia(true)
  localStorage.removeItem('harbormaster-columns:tasks-phone')
})
afterEach(() => restorePhone())

describe('Tasks on a phone', () => {
  const sample = () => [
    makeTask({ serviceName: 'svc-a' }),
    makeTask({ serviceName: 'svc-b', clusterName: 'cluster-b' }),
    makeTask({ serviceName: 'svc-c', health: 'UNHEALTHY' }),
    stoppedError({ serviceName: 'svc-d' }),
  ]

  it('state chips carry the counts and replace the segmented filter', async () => {
    serve(sample())
    renderApp('/tasks')
    const chips = await screen.findByRole('group', { name: 'Task filter' })
    expect(await within(chips).findByRole('button', { name: 'Problems 2', pressed: true })).toBeInTheDocument()
    expect(within(chips).getByRole('button', { name: 'Running 3' })).toBeInTheDocument()
    expect(within(chips).getByRole('button', { name: 'Stopped 1' })).toBeInTheDocument()
    expect(within(chips).getByRole('button', { name: 'All 4' })).toBeInTheDocument()
    expect(screen.queryByRole('radiogroup', { name: 'Task filter' })).not.toBeInTheDocument()
    await userEvent.click(within(chips).getByRole('button', { name: 'All 4' }))
    await waitFor(() => expect(rows()).toHaveLength(4))
  })

  it('rows show the friendly stop reason and leave the desktop table tools out', async () => {
    serve([stoppedError()])
    renderApp('/tasks')
    const row = (await screen.findAllByRole('row'))[0]
    expect(within(row).getByText(/Out of memory \(exit 137\)/)).toBeInTheDocument()
    expect(screen.queryByRole('columnheader')).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Columns' })).not.toBeInTheDocument()
    expect(screen.queryByRole('checkbox', { name: 'Group repeats' })).not.toBeInTheDocument()
  })

  it('filters sheet applies a service filter, summarises it and clears it', async () => {
    serve(sample())
    renderApp('/tasks?filter=all')
    await waitFor(() => expect(rows()).toHaveLength(4))
    await userEvent.click(screen.getByRole('button', { name: 'Filters' }))
    const sheet = await screen.findByRole('dialog', { name: 'Filters and fields' })
    fireEvent.change(within(sheet).getByRole('combobox', { name: 'Service' }), { target: { value: 'svc-b' } })
    await userEvent.click(within(sheet).getByRole('button', { name: 'Done' }))
    await waitFor(() => expect(rows()).toHaveLength(1))
    expect(screen.getByText('Filters: Service svc-b')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Filters (1)' })).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'Clear filters' }))
    await waitFor(() => expect(rows()).toHaveLength(4))
    expect(screen.queryByText(/^Filters: /)).not.toBeInTheDocument()
  })

  it('an empty result offers Clear filters', async () => {
    serve(sample())
    renderApp('/tasks?filter=running&q=nothing-matches')
    expect(await screen.findByText('No tasks match.')).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'Clear filters' }))
    await waitFor(() => expect(rows()).toHaveLength(4))
  })

  it('a load failure shows its message with Retry', async () => {
    server.use(http.get('/api/tasks', () => HttpResponse.json({ error: { code: 'aws_error', message: 'The ECS request timed out.' } }, { status: 502 })))
    renderApp('/tasks')
    expect(await screen.findByRole('button', { name: 'Retry' })).toBeInTheDocument()
  })

  describe('row action sheet', () => {
    const openSheet = async (task: TaskDTO) => {
      await userEvent.click(await screen.findByRole('button', { name: `Actions for task ${shortId(task)}` }))
      return screen.findByRole('menu', { name: `Actions for task ${shortId(task)}` })
    }

    it('lists Details, Logs, Terminal and Stop task for an operator who can exec', async () => {
      const task = makeTask()
      serve([task])
      renderApp('/tasks?filter=running')
      const menu = await openSheet(task)
      expect(within(menu).getByRole('menuitem', { name: 'Details' })).toBeInTheDocument()
      expect(within(menu).getByRole('menuitem', { name: 'Logs' })).toHaveAttribute('href', `/services/${task.service!.id}/logs`)
      expect(within(menu).getByRole('menuitem', { name: 'Terminal' })).toHaveAttribute('href', expect.stringContaining('/terminal?task='))
      expect(within(menu).getByRole('menuitem', { name: 'Stop task' })).toBeEnabled()
    })

    it('leaves Terminal out without the permission and Stop out without operate', async () => {
      const task = makeTask({ can: { operate: false, terminal: false } })
      serve([task])
      renderApp('/tasks?filter=running')
      const menu = await openSheet(task)
      expect(within(menu).queryByRole('menuitem', { name: /Terminal/ })).not.toBeInTheDocument()
      expect(within(menu).queryByRole('menuitem', { name: /Stop task/ })).not.toBeInTheDocument()
    })

    it('shows Terminal disabled with its reason when ECS Exec is off', async () => {
      const task = makeTask({ exec_enabled: false })
      serve([task])
      renderApp('/tasks?filter=running')
      const menu = await openSheet(task)
      const terminal = within(menu).getByRole('menuitem', { name: /Terminal/ })
      expect(terminal).toBeDisabled()
      expect(terminal).toHaveTextContent('ECS Exec is off for this task')
    })

    it('Details opens the task sheet and Stop task asks for confirmation with the existing text', async () => {
      const task = makeTask()
      serve([task])
      renderApp('/tasks?filter=running')
      await userEvent.click(within(await openSheet(task)).getByRole('menuitem', { name: 'Details' }))
      const details = await screen.findByRole('dialog', { name: shortId(task) })
      expect(within(details).getByText(task.id)).toBeInTheDocument()
      await userEvent.click(within(details).getByRole('button', { name: 'Close panel' }))

      await userEvent.click(within(await openSheet(task)).getByRole('menuitem', { name: 'Stop task' }))
      expect(await screen.findByRole('heading', { name: `Stop task ${shortId(task)}?` })).toBeInTheDocument()
    })
  })

  describe('hidden-stopped note', () => {
    it('keeps the Change link on a tap-sized target and can be dismissed', async () => {
      serve(sample())
      renderApp('/tasks')
      expect(await screen.findByText('Stopped tasks older than 10 min are hidden.')).toBeInTheDocument()
      expect(screen.getByRole('link', { name: 'Change' })).toHaveAttribute('data-hm-rl')
      await userEvent.click(screen.getByRole('button', { name: 'Dismiss' }))
      expect(screen.queryByText('Stopped tasks older than 10 min are hidden.')).not.toBeInTheDocument()
    })
  })
})
