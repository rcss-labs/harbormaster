import { fireEvent, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { describe, expect, it } from 'vitest'
import type { TaskDTO } from '../../api/types'
import { ALL_TASKS, ME_OMER, TASKS, tasksList } from '../../test/fixtures'
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

describe('Tasks summary windows', () => {
  it('labels By status with the stopped-tasks window and Why tasks stopped with 72 h', async () => {
    renderApp('/tasks')
    const byStatus = (await screen.findByRole('heading', { name: 'By status' })).parentElement!
    expect(within(byStatus).getByText('Stopped: last 10 min')).toBeInTheDocument()
    const reasons = screen.getByRole('heading', { name: 'Why tasks stopped' }).parentElement!
    expect(within(reasons).getByText('Last 72 h')).toBeInTheDocument()
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

describe('Tasks running by cluster', () => {
  /** n clusters; cluster i runs i tasks (half of them Spot), so the highest number is the last one. */
  const clusterCapacities = (count: number) =>
    Array.from({ length: count }, (_, index) => {
      const total = index + 1
      return { id: `id-${total}`, name: `cluster-${String(total).padStart(2, '0')}`, fargate: Math.ceil(total / 2), spot: Math.floor(total / 2) }
    })

  const withClusters = (count: number) =>
    server.use(
      http.get('/api/tasks', () =>
        HttpResponse.json({ ...TASKS, summary: { ...TASKS.summary, by_cluster: clusterCapacities(count) } }),
      ),
    )

  const card = async () => (await screen.findByRole('heading', { name: 'Running by cluster' })).closest('div')!.parentElement!

  it('shows the 10 busiest clusters, highest first, and a View all button when there are more', async () => {
    withClusters(12)
    renderApp('/tasks')
    const section = within(await card())
    const names = section.getAllByRole('link').map((link) => link.textContent)
    expect(names).toEqual(['cluster-12', 'cluster-11', 'cluster-10', 'cluster-09', 'cluster-08', 'cluster-07', 'cluster-06', 'cluster-05', 'cluster-04', 'cluster-03'])
    expect(section.getByRole('button', { name: 'View all (12)' })).toBeInTheDocument()
  })

  it('breaks ties by name and hides View all at 10 clusters or fewer', async () => {
    server.use(
      http.get('/api/tasks', () =>
        HttpResponse.json({
          ...TASKS,
          summary: {
            ...TASKS.summary,
            by_cluster: [
              { id: 'b', name: 'beta', fargate: 2, spot: 0 },
              { id: 'a', name: 'alpha', fargate: 1, spot: 1 },
              { id: 'c', name: 'gamma', fargate: 5, spot: 0 },
            ],
          },
        }),
      ),
    )
    renderApp('/tasks')
    const section = within(await card())
    expect(section.getAllByRole('link').map((link) => link.textContent)).toEqual(['gamma', 'alpha', 'beta'])
    expect(section.queryByRole('button', { name: /View all/ })).not.toBeInTheDocument()
  })

  it('View all opens a floating window with every cluster, without extending the card', async () => {
    withClusters(12)
    renderApp('/tasks')
    const section = within(await card())
    await userEvent.click(section.getByRole('button', { name: 'View all (12)' }))
    const dialog = await screen.findByRole('dialog', { name: 'Running by cluster' })
    expect(within(dialog).getAllByRole('link')).toHaveLength(12)
    expect(within(dialog).getByRole('link', { name: 'cluster-01' })).toHaveAttribute('href', '/clusters/id-1')
    expect(within(dialog).getByText('Fargate Spot')).toBeInTheDocument()
    expect(section.getAllByRole('link')).toHaveLength(10)
  })

  it.each([
    ['the Close button', async (dialog: HTMLElement) => userEvent.click(within(dialog).getByRole('button', { name: 'Close' }))],
    ['a click outside the window', async (dialog: HTMLElement) => userEvent.click(dialog.parentElement!)],
    ['Escape', async () => userEvent.keyboard('{Escape}')],
  ])('closes the window on %s', async (_name, close) => {
    withClusters(12)
    renderApp('/tasks')
    await userEvent.click(within(await card()).getByRole('button', { name: 'View all (12)' }))
    const dialog = await screen.findByRole('dialog', { name: 'Running by cluster' })
    await close(dialog)
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
  })

  it('does not close when clicking inside the window', async () => {
    withClusters(12)
    renderApp('/tasks')
    await userEvent.click(within(await card()).getByRole('button', { name: 'View all (12)' }))
    const dialog = await screen.findByRole('dialog', { name: 'Running by cluster' })
    await userEvent.click(within(dialog).getByText('Fargate'))
    expect(screen.getByRole('dialog')).toBeInTheDocument()
  })
})

describe('Tasks grouping', () => {
  const toggle = (name: string) => screen.getByRole('button', { name })
  const paymentsRows = () => screen.queryAllByRole('link', { name: 'payments-api' })

  it('groups by account, region and cluster, all expanded by default, with counts', async () => {
    renderApp('/tasks?filter=running')
    await screen.findByRole('button', { name: 'homelab-admin' })
    for (const name of ['homelab-admin', 'staging-example', 'us-east-1', 'eu-west-1', 'eu-central-1', 'core-services', 'eu-apps', 'edge-proxy']) {
      const buttons = screen.getAllByRole('button', { name })
      expect(buttons.length).toBeGreaterThan(0)
      for (const button of buttons) expect(button).toHaveAttribute('aria-expanded', 'true')
    }
    expect(paymentsRows().length).toBeGreaterThan(0)
    const coreRunning = TASKS.items.filter((task) => task.cluster.name === 'core-services' && task.status !== 'STOPPED').length
    expect(toggle('core-services').closest('tr')).toHaveTextContent(`${coreRunning} task${coreRunning === 1 ? '' : 's'}`)
  })

  it('collapses and expands each level on its own', async () => {
    renderApp('/tasks?filter=running')
    await screen.findByRole('button', { name: 'core-services' })

    await userEvent.click(toggle('core-services'))
    expect(toggle('core-services')).toHaveAttribute('aria-expanded', 'false')
    expect(paymentsRows()).toHaveLength(0)
    expect(screen.getAllByRole('link', { name: 'catalog-api' }).length).toBeGreaterThan(0)

    await userEvent.click(toggle('core-services'))
    expect(paymentsRows().length).toBeGreaterThan(0)

    // Collapsing the region hides its clusters, and the cluster keeps its own state underneath.
    await userEvent.click(screen.getAllByRole('button', { name: 'us-east-1' })[0])
    expect(screen.queryByRole('button', { name: 'core-services' })).not.toBeInTheDocument()
    expect(paymentsRows()).toHaveLength(0)
    await userEvent.click(screen.getAllByRole('button', { name: 'us-east-1' })[0])
    expect(paymentsRows().length).toBeGreaterThan(0)

    await userEvent.click(toggle('homelab-admin'))
    expect(screen.queryByRole('button', { name: 'eu-west-1' })).not.toBeInTheDocument()
    expect(screen.queryAllByRole('link', { name: 'catalog-api' })).toHaveLength(0)
  })

  it('Collapse all and Expand all', async () => {
    renderApp('/tasks?filter=running')
    await screen.findByRole('button', { name: 'core-services' })
    await userEvent.click(screen.getByRole('button', { name: 'Collapse all' }))
    expect(screen.getByRole('button', { name: 'homelab-admin' })).toHaveAttribute('aria-expanded', 'false')
    expect(screen.queryByRole('button', { name: 'core-services' })).not.toBeInTheDocument()
    expect(paymentsRows()).toHaveLength(0)

    await userEvent.click(screen.getByRole('button', { name: 'Expand all' }))
    expect(toggle('core-services')).toHaveAttribute('aria-expanded', 'true')
    expect(paymentsRows().length).toBeGreaterThan(0)
  })
})
