import { fireEvent, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { describe, expect, it } from 'vitest'
import type { TaskDTO } from '../../api/types'
import { ALL_TASKS, ME_OMER, tasksList } from '../../test/fixtures'
import { server } from '../../test/msw'
import { renderApp } from '../../test/renderApp'

const runningTemplate = ALL_TASKS.find((task) => task.service && task.status === 'RUNNING' && task.health === 'HEALTHY')!

type TaskOverrides = Partial<TaskDTO> & { serviceName?: string; clusterName?: string }

let counter = 0
/** A task cloned from a real fixture task; every call gets a unique id, short id and IP. */
function makeTask(overrides: TaskOverrides): TaskDTO {
  const { serviceName = 'svc-a', clusterName = 'cluster-a', ...rest } = overrides
  counter++
  const n = String(counter).padStart(4, '0')
  return {
    ...runningTemplate,
    id: `task-id-${n}`,
    short_id: `abcdef${n}0000`,
    arn: `arn:aws:ecs:eu-west-1:1:task/${clusterName}/${n}`,
    private_ip: `10.9.${Math.floor(counter / 200)}.${counter % 200}`,
    service: { ...runningTemplate.service!, id: `svc-id-${serviceName}`, name: serviceName },
    cluster: { id: `cluster-id-${clusterName}`, name: clusterName },
    started_at: new Date(Date.now() - 3_600_000).toISOString(),
    ...rest,
  }
}
const stoppedError = (extra: TaskOverrides = {}) =>
  makeTask({
    status: 'STOPPED',
    exit_code: 137,
    health: 'UNKNOWN',
    stop_code: 'EssentialContainerExited',
    stopped_reason: 'OutOfMemoryError',
    stop_reason_group: 'Out of memory (exit 137)',
    stopped_at: new Date(Date.now() - 120_000).toISOString(),
    ...extra,
  })
const stoppedNormal = (extra: TaskOverrides = {}) =>
  makeTask({
    status: 'STOPPED',
    exit_code: 0,
    health: 'UNKNOWN',
    stop_code: 'ServiceSchedulerInitiated',
    stopped_reason: 'Scaling activity',
    stop_reason_group: 'Scaled in',
    stopped_at: new Date(Date.now() - 240_000).toISOString(),
    ...extra,
  })
const unhealthy = (extra: TaskOverrides = {}) => makeTask({ health: 'UNHEALTHY', ...extra })
const healthy = (extra: TaskOverrides = {}) => makeTask({ health: 'HEALTHY', ...extra })

function serve(items: TaskDTO[]) {
  server.use(http.get('/api/tasks', () => HttpResponse.json(tasksList(items))))
}

const taskRows = () => within(screen.getByRole('table', { name: 'Tasks' })).getAllByRole('row').slice(1)
const shortId = (task: TaskDTO) => task.short_id.slice(0, 12)
const search = () => screen.getByTestId('search').textContent ?? ''

describe('Tasks segments', () => {
  const sample = () => [
    healthy(),
    healthy({ serviceName: 'svc-b' }),
    unhealthy({ serviceName: 'svc-c' }),
    stoppedError({ serviceName: 'svc-d' }),
    stoppedError({ serviceName: 'svc-e' }),
    stoppedNormal({ serviceName: 'svc-f' }),
  ]

  it('shows Problems / Running / Stopped / All with counts, Problems first', async () => {
    serve(sample())
    renderApp('/tasks?keep=1')
    expect(await screen.findByRole('radio', { name: 'Problems 3' })).toBeChecked()
    expect(screen.getByRole('radio', { name: 'Running 3' })).toBeInTheDocument()
    expect(screen.getByRole('radio', { name: 'Stopped 3' })).toBeInTheDocument()
    expect(screen.getByRole('radio', { name: 'All 6' })).toBeInTheDocument()
    await waitFor(() => expect(taskRows()).toHaveLength(3))
  })

  it('switching segment lists that segment and sets ?filter= keeping other params', async () => {
    serve(sample())
    renderApp('/tasks?keep=1')
    fireEvent.click(await screen.findByRole('radio', { name: 'Stopped 3' }))
    await waitFor(() => expect(search()).toContain('filter=stopped'))
    expect(search()).toContain('keep=1')
    await waitFor(() => expect(taskRows()).toHaveLength(3))
    fireEvent.click(screen.getByRole('radio', { name: 'All 6' }))
    await waitFor(() => expect(taskRows()).toHaveLength(6))
  })

  it('shows KPI strip cells for running, unhealthy and stopped tasks', async () => {
    renderApp('/tasks')
    const strip = await screen.findByRole('list', { name: 'Task summary' })
    for (const label of ['Running', 'Unhealthy', 'Stopped, error', 'Stopped, normal', 'On spot']) {
      expect(within(strip).getByText(label)).toBeInTheDocument()
    }
    expect(within(strip).getAllByText(/last 10 min/).length).toBeGreaterThan(0)
  })
})

describe('Tasks query', () => {
  it('matches task ID, service, cluster or IP and updates the counts and URL', async () => {
    serve([
      healthy({ serviceName: 'billing-api', clusterName: 'alpha' }),
      healthy({ serviceName: 'other-a', clusterName: 'zeta-cluster' }),
      healthy({ serviceName: 'other-b', clusterName: 'beta', private_ip: '10.77.7.7' }),
      healthy({ serviceName: 'other-c', clusterName: 'beta', short_id: 'feedbeef1234' }),
    ])
    renderApp('/tasks?filter=running')
    const input = await screen.findByRole('searchbox', { name: 'Filter tasks' })
    await waitFor(() => expect(taskRows()).toHaveLength(4))

    await userEvent.type(input, 'billing')
    await waitFor(() => expect(taskRows()).toHaveLength(1))
    expect(screen.getByRole('radio', { name: 'Running 1' })).toBeInTheDocument()
    expect(search()).toContain('q=billing')

    await userEvent.clear(input)
    await userEvent.type(input, 'zeta-clu')
    await waitFor(() => expect(taskRows()).toHaveLength(1))
    expect(within(taskRows()[0]).getByText('other-a')).toBeInTheDocument()

    await userEvent.clear(input)
    await userEvent.type(input, '10.77.7.7')
    await waitFor(() => expect(taskRows()).toHaveLength(1))
    expect(within(taskRows()[0]).getByText('other-b')).toBeInTheDocument()

    await userEvent.clear(input)
    await userEvent.type(input, 'feedbeef')
    await waitFor(() => expect(taskRows()).toHaveLength(1))
    expect(within(taskRows()[0]).getByText('other-c')).toBeInTheDocument()
  })

  it('shows an empty message when nothing matches', async () => {
    serve([healthy()])
    renderApp('/tasks?filter=running&q=nomatch')
    expect(await screen.findByText('No tasks match.')).toBeInTheDocument()
  })
})

describe('Tasks group repeats', () => {
  it('is on by default and collapses same service + stop reason into one xN row', async () => {
    const repeated = [stoppedError(), stoppedError(), stoppedError()]
    const otherReason = stoppedError({ stop_reason_group: 'Failed health checks', stopped_reason: 'Task failed ELB health checks' })
    const otherService = stoppedError({ serviceName: 'svc-z' })
    serve([...repeated, otherReason, otherService])
    renderApp('/tasks')
    expect(await screen.findByRole('checkbox', { name: 'Group repeats' })).toBeChecked()
    await waitFor(() => expect(taskRows()).toHaveLength(3))
    expect(screen.getByRole('button', { name: /×3 tasks/ })).toHaveAttribute('aria-expanded', 'false')
    expect(screen.queryByText(shortId(repeated[0]))).not.toBeInTheDocument()
    expect(screen.getByText(shortId(otherReason))).toBeInTheDocument()
    expect(screen.getByText(shortId(otherService))).toBeInTheDocument()
  })

  it('groups unhealthy running tasks by service + health and keeps different health apart', async () => {
    serve([unhealthy(), unhealthy(), healthy(), healthy()])
    renderApp('/tasks?filter=running')
    await waitFor(() => expect(taskRows()).toHaveLength(2))
    expect(screen.getAllByRole('button', { name: /×2 tasks/ })).toHaveLength(2)
  })

  it('expands a group to its tasks and records it in the URL', async () => {
    const repeated = [stoppedError(), stoppedError(), stoppedError()]
    serve(repeated)
    renderApp('/tasks')
    await userEvent.click(await screen.findByRole('button', { name: /×3 tasks/ }))
    expect(screen.getByRole('button', { name: /×3 tasks/ })).toHaveAttribute('aria-expanded', 'true')
    for (const task of repeated) expect(screen.getByText(shortId(task))).toBeInTheDocument()
    expect(taskRows()).toHaveLength(4)
    expect(search()).toContain('open=')

    await userEvent.click(screen.getByRole('button', { name: /×3 tasks/ }))
    expect(taskRows()).toHaveLength(1)
    expect(search()).not.toContain('open=')
  })

  it('restores open groups from the URL', async () => {
    const repeated = [stoppedError(), stoppedError()]
    serve(repeated)
    const { unmount } = renderApp('/tasks')
    await userEvent.click(await screen.findByRole('button', { name: /×2 tasks/ }))
    const url = `/tasks?${search()}`
    unmount()
    renderApp(url)
    expect(await screen.findByText(shortId(repeated[0]))).toBeInTheDocument()
  })

  it('turning it off lists every task and sets group=0; turning it on again clears it', async () => {
    serve([stoppedError(), stoppedError(), stoppedError()])
    renderApp('/tasks')
    await userEvent.click(await screen.findByRole('checkbox', { name: 'Group repeats' }))
    await waitFor(() => expect(taskRows()).toHaveLength(3))
    expect(search()).toContain('group=0')
    expect(screen.queryByRole('button', { name: /×3 tasks/ })).not.toBeInTheDocument()
    await userEvent.click(screen.getByRole('checkbox', { name: 'Group repeats' }))
    await waitFor(() => expect(taskRows()).toHaveLength(1))
    expect(search()).not.toContain('group=')
  })

  it('bounds an expanded group by the page size', async () => {
    serve(Array.from({ length: 120 }, () => stoppedError()))
    renderApp('/tasks')
    await userEvent.click(await screen.findByRole('button', { name: /×120 tasks/ }))
    expect(taskRows()).toHaveLength(50)
    expect(screen.getByText('1–50 of 121')).toBeInTheDocument()
  })
})

describe('Tasks paging', () => {
  const many = (count: number) => Array.from({ length: count }, (_, i) => healthy({ serviceName: `svc-${String(i).padStart(3, '0')}` }))

  it('shows 50 rows per page; groups count as one row each', async () => {
    serve(many(120))
    renderApp('/tasks?filter=running')
    expect(await screen.findByText('1–50 of 120')).toBeInTheDocument()
    expect(taskRows()).toHaveLength(50)
    await userEvent.click(screen.getByRole('button', { name: 'Next page' }))
    expect(await screen.findByText('51–100 of 120')).toBeInTheDocument()
    expect(search()).toContain('page=2')
  })

  it('reads the page from the URL and resets to page 1 when the segment changes', async () => {
    serve(many(120))
    renderApp('/tasks?filter=running&page=3')
    expect(await screen.findByText('101–120 of 120')).toBeInTheDocument()
    expect(taskRows()).toHaveLength(20)
    fireEvent.click(screen.getByRole('radio', { name: /^All/ }))
    await waitFor(() => expect(search()).not.toContain('page='))
  })

  it('loads every API page so grouping and paging see the whole filtered set', async () => {
    const all = many(7)
    server.use(
      http.get('/api/tasks', ({ request }) => {
        const cursor = new URL(request.url).searchParams.get('cursor')
        const start = cursor ? Number(cursor) : 0
        return HttpResponse.json({ ...tasksList(all), items: all.slice(start, start + 3), next: start + 3 < all.length ? String(start + 3) : null })
      }),
    )
    renderApp('/tasks?filter=running')
    await waitFor(() => expect(taskRows()).toHaveLength(7))
  })
})

describe('Tasks URL round trip', () => {
  it('restores segment, query, grouping and page from the URL', async () => {
    serve([...Array.from({ length: 60 }, (_, i) => stoppedError({ serviceName: `svc-${String(i).padStart(3, '0')}` })), healthy()])
    renderApp('/tasks?filter=stopped&q=svc-&group=0&page=2')
    expect(await screen.findByRole('radio', { name: /^Stopped/ })).toBeChecked()
    expect(screen.getByRole('searchbox', { name: 'Filter tasks' })).toHaveValue('svc-')
    expect(screen.getByRole('checkbox', { name: 'Group repeats' })).not.toBeChecked()
    expect(await screen.findByText('51–60 of 60')).toBeInTheDocument()
  })
})

describe('Tasks rows', () => {
  it('error-exit row has the bad stripe and "Stopped (exit 137)"', async () => {
    serve([stoppedError()])
    renderApp('/tasks')
    const [status] = await screen.findAllByText('Stopped (exit 137)')
    expect(status.closest('[role="row"]')).toHaveAttribute('data-status', 'bad')
  })

  it('unhealthy running row has the warn stripe', async () => {
    serve([unhealthy()])
    renderApp('/tasks')
    const row = within(await screen.findByRole('table', { name: 'Tasks' })).getByText('Unhealthy').closest('[role="row"]')!
    expect(row).toHaveAttribute('data-status', 'warn')
  })

  it('standalone row shows group and started_by', async () => {
    renderApp('/tasks?filter=running')
    const row = (await screen.findByText('family:release-build')).closest('[role="row"]') as HTMLElement
    expect(within(row).getByText('Standalone · started by events-rule/nightly-release')).toBeInTheDocument()
  })

  it('Stop opens the confirm dialog', async () => {
    serve([healthy()])
    renderApp('/tasks?filter=running')
    await userEvent.click(await screen.findByRole('button', { name: 'Stop' }))
    expect(await screen.findByRole('dialog')).toBeInTheDocument()
  })
})

describe('Tasks hidden-stopped note', () => {
  it('shows the hidden-tasks note with a Change link for admins', async () => {
    renderApp('/tasks')
    expect(await screen.findByText('Stopped tasks older than 10 min are hidden.')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Change' })).toHaveAttribute('href', '/settings/general')
  })

  it('non-admins see the note without the link', async () => {
    server.use(http.get('/api/me', () => HttpResponse.json(ME_OMER)))
    renderApp('/tasks')
    expect(await screen.findByText('Stopped tasks older than 10 min are hidden.')).toBeInTheDocument()
    expect(screen.queryByRole('link', { name: 'Change' })).not.toBeInTheDocument()
  })
})

describe('Tasks Terminal link', () => {
  const serviceRunning = ALL_TASKS.find((task) => task.service && task.status === 'RUNNING')!
  const standaloneRunning = ALL_TASKS.find((task) => !task.service && task.status === 'RUNNING')!
  const rowOf = (text: string) => screen.findByText(text).then((cell) => cell.closest('[role="row"]') as HTMLElement)

  it('shows Terminal on a running service row when exec is on and user has Terminal', async () => {
    serve([{ ...serviceRunning, exec_enabled: true, can: { ...serviceRunning.can, terminal: true } }])
    renderApp('/tasks?filter=running')
    const link = await screen.findByRole('link', { name: 'Terminal' })
    expect(link).toHaveAttribute('href', `/services/${serviceRunning.service!.id}/terminal?task=${encodeURIComponent(serviceRunning.id)}`)
  })

  it('hides Terminal when exec is off', async () => {
    serve([{ ...serviceRunning, exec_enabled: false, can: { ...serviceRunning.can, terminal: true } }])
    renderApp('/tasks?filter=running')
    const row = await rowOf(serviceRunning.service!.name)
    expect(within(row).getByRole('button', { name: 'Stop' })).toBeInTheDocument()
    expect(within(row).queryByRole('link', { name: 'Terminal' })).not.toBeInTheDocument()
  })

  it('hides Terminal without Terminal permission', async () => {
    serve([{ ...serviceRunning, exec_enabled: true, can: { ...serviceRunning.can, terminal: false } }])
    renderApp('/tasks?filter=running')
    const row = await rowOf(serviceRunning.service!.name)
    expect(within(row).getByRole('button', { name: 'Stop' })).toBeInTheDocument()
    expect(within(row).queryByRole('link', { name: 'Terminal' })).not.toBeInTheDocument()
  })

  it('hides Stop without operate permission', async () => {
    serve([{ ...serviceRunning, can: { operate: false, terminal: false } }])
    renderApp('/tasks?filter=running')
    const row = await rowOf(serviceRunning.service!.name)
    expect(within(row).queryByRole('button', { name: 'Stop' })).not.toBeInTheDocument()
  })

  it('no Terminal or Stop on stopped rows', async () => {
    serve([{ ...serviceRunning, status: 'STOPPED', exit_code: 0, exec_enabled: true, can: { ...serviceRunning.can, terminal: true } }])
    renderApp('/tasks?filter=stopped')
    const row = await rowOf(serviceRunning.service!.name)
    expect(within(row).queryByRole('link', { name: 'Terminal' })).not.toBeInTheDocument()
    expect(within(row).queryByRole('button', { name: 'Stop' })).not.toBeInTheDocument()
  })

  it('shows Terminal on a running standalone row, linking to the standalone terminal page', async () => {
    serve([{ ...standaloneRunning, exec_enabled: true, can: { ...standaloneRunning.can, terminal: true } }])
    renderApp('/tasks?filter=running')
    const row = await rowOf(`family:${standaloneRunning.family}`)
    expect(within(row).getByRole('link', { name: 'Terminal' })).toHaveAttribute(
      'href',
      `/tasks/${encodeURIComponent(standaloneRunning.short_id)}/terminal`,
    )
  })

  it('hides Terminal on a standalone row when the task was started without exec', async () => {
    serve([{ ...standaloneRunning, exec_enabled: false, can: { ...standaloneRunning.can, terminal: true } }])
    renderApp('/tasks?filter=running')
    const row = await rowOf(`family:${standaloneRunning.family}`)
    expect(within(row).getByRole('button', { name: 'Stop' })).toBeInTheDocument()
    expect(within(row).queryByRole('link', { name: 'Terminal' })).not.toBeInTheDocument()
  })
})
