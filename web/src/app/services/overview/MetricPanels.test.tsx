import userEvent from '@testing-library/user-event'
import { screen, within } from '@testing-library/react'
import { http, HttpResponse } from 'msw'
import { describe, expect, it } from 'vitest'
import type { ServiceMetricsDTO, TargetHealthDTO } from '../../../api/types'
import {
  FIXTURE_NOW,
  SERVICE_DETAILS,
  SERVICE_METRICS_CATALOG_NO_INSIGHTS,
  SERVICE_METRICS_PAYMENTS,
  SERVICE_NOTIFICATIONS,
  SERVICE_PAYMENTS,
} from '../../../test/fixtures'
import { server } from '../../../test/msw'
import { renderWithProviders } from '../../../test/setup'
import { ALBPanels, HealthCard, RunningTasksMeters, ServiceKpis, UsageChart } from './MetricPanels'

const CATALOG = Object.values(SERVICE_DETAILS).find((service) => service.name === 'catalog-api')!
const MS_PER_HOUR = 3_600_000

describe('ServiceKpis', () => {
  const cell = (label: string) => screen.getByText(label, { selector: 'span' }).closest('li') as HTMLElement

  it('shows tasks, revision, usage percentages, load balancer numbers and sparklines', () => {
    const { container } = renderWithProviders(<ServiceKpis service={SERVICE_PAYMENTS} metrics={SERVICE_METRICS_PAYMENTS} />)
    expect(cell('Tasks')).toHaveTextContent(`${SERVICE_PAYMENTS.running}/${SERVICE_PAYMENTS.desired}`)
    expect(cell('Revision')).toHaveTextContent(`:${SERVICE_PAYMENTS.revision}`)
    expect(cell('CPU')).toHaveTextContent(`${Math.round(SERVICE_METRICS_PAYMENTS.now!.cpu!)}%`)
    expect(cell('CPU')).toHaveTextContent('of 512 units')
    expect(cell('Memory')).toHaveTextContent('of 1024 MiB')
    expect(cell('Req / min')).toHaveTextContent('438')
    expect(cell('p95')).toHaveTextContent('171')
    expect(cell('HTTP 5xx')).toHaveTextContent('3today')
    expect(container.querySelectorAll('svg')).toHaveLength(4)
  })

  it('flags missing tasks', () => {
    renderWithProviders(<ServiceKpis service={{ ...SERVICE_PAYMENTS, running: SERVICE_PAYMENTS.desired - 1 }} metrics={SERVICE_METRICS_PAYMENTS} />)
    expect(cell('Tasks')).toHaveTextContent('1 pending')
  })

  it('shows a dash and a note when Container Insights is off, and omits load balancer cells without a load balancer', () => {
    const noLoadBalancer = { ...CATALOG, networking: { ...CATALOG.networking, load_balancers: [] } }
    renderWithProviders(<ServiceKpis service={noLoadBalancer} metrics={SERVICE_METRICS_CATALOG_NO_INSIGHTS} />)
    expect(screen.getAllByText('Container Insights off')).toHaveLength(2)
    expect(screen.queryByText('Req / min')).not.toBeInTheDocument()
  })
})

describe('UsageChart', () => {
  it('maps markers to the 24 h axis', () => {
    const metrics: ServiceMetricsDTO = {
      ...SERVICE_METRICS_PAYMENTS,
      markers: [{ at: new Date(Date.parse(FIXTURE_NOW) - 12 * MS_PER_HOUR).toISOString(), label: ':42', kind: 'deploy', failed: false }],
    }
    const { container } = renderWithProviders(<UsageChart service={SERVICE_PAYMENTS} metrics={metrics} now={Date.parse(FIXTURE_NOW)} />)
    const label = screen.getByText(':42')
    expect(parseFloat(label.style.left)).toBeCloseTo(50, 0)
    expect(container.querySelector('a[href$="/metrics"]')).toHaveTextContent('All metrics →')
    expect(screen.getByText('Deployment or restart')).toBeInTheDocument()
  })

  it('Insights off banner', () => {
    renderWithProviders(<UsageChart service={CATALOG} metrics={SERVICE_METRICS_CATALOG_NO_INSIGHTS} now={Date.parse(FIXTURE_NOW)} />)
    expect(
      screen.getByText(`Container Insights is off for ${CATALOG.cluster.name}, so there are no CPU and memory graphs.`),
    ).toBeInTheDocument()
  })

  it('says there is no data when every point is null', () => {
    const metrics: ServiceMetricsDTO = {
      ...SERVICE_METRICS_PAYMENTS,
      series: { ...SERVICE_METRICS_PAYMENTS.series!, cpu: [null, null], mem: [null] },
    }
    renderWithProviders(<UsageChart service={SERVICE_PAYMENTS} metrics={metrics} now={Date.parse(FIXTURE_NOW)} />)
    expect(screen.getByText('No data yet')).toBeInTheDocument()
  })
})

describe('HealthCard', () => {
  it('target health donut label 1/2 healthy', async () => {
    const health: TargetHealthDTO = {
      groups: [{ target_group_arn: 'arn:tg', name: 'catalog-api-tg', healthy: 1, unhealthy: 1, targets: [] }],
      healthy: 1,
      unhealthy: 1,
      polled_at: FIXTURE_NOW,
    }
    server.use(http.get('/api/services/:id/target-health', () => HttpResponse.json(health)))
    renderWithProviders(<HealthCard service={CATALOG}>deploy</HealthCard>)
    expect(await screen.findByText('1/2')).toBeInTheDocument()
    expect(screen.getByText('healthy')).toBeInTheDocument()
    expect(screen.getByText('catalog-api-tg')).toBeInTheDocument()
    expect(screen.getByText('Unhealthy targets')).toBeInTheDocument()
    expect(screen.getByText('Missing tasks').parentElement).toHaveTextContent('0')
  })
})

describe('ALBPanels', () => {
  it('rendered only with a load balancer', () => {
    const noLoadBalancer = { ...SERVICE_NOTIFICATIONS, networking: { ...SERVICE_NOTIFICATIONS.networking, load_balancers: [] } }
    const without = renderWithProviders(<ALBPanels service={noLoadBalancer} metrics={SERVICE_METRICS_PAYMENTS} />)
    expect(screen.queryByText('Requests per minute, 24 h')).not.toBeInTheDocument()
    without.unmount()
    renderWithProviders(<ALBPanels service={SERVICE_PAYMENTS} metrics={SERVICE_METRICS_PAYMENTS} />)
    expect(screen.getByText('Requests per minute, 24 h')).toBeInTheDocument()
    expect(screen.getByText('Response time p95, 24 h')).toBeInTheDocument()
    expect(screen.getByText('now 438')).toBeInTheDocument()
    expect(screen.getByText('now 171 ms')).toBeInTheDocument()
    expect(screen.getByText('3 today')).toBeInTheDocument()
  })
})

describe('RunningTasksMeters', () => {
  it('shows per-task meters when tasks_now is present', () => {
    // The fixture's task ids are placeholders; key the usage to the service's real running tasks.
    const metrics: ServiceMetricsDTO = {
      ...SERVICE_METRICS_PAYMENTS,
      tasks_now: SERVICE_PAYMENTS.running_tasks.map((task, index) => ({ task_id: task.short_id, cpu: 31.2 + index, mem: 57.4 })),
    }
    renderWithProviders(<RunningTasksMeters service={SERVICE_PAYMENTS} metrics={metrics} />)
    const card = screen.getByRole('region', { name: 'Running tasks' })
    expect(within(card).getByText('CPU')).toBeInTheDocument()
    expect(within(card).getByText('Memory')).toBeInTheDocument()
    expect(within(card).getAllByText('57%')).toHaveLength(SERVICE_PAYMENTS.running_tasks.length)
    expect(within(card).getByText('31%')).toBeInTheDocument()
  })

  it('falls back to plain rows without tasks_now', () => {
    renderWithProviders(<RunningTasksMeters service={SERVICE_PAYMENTS} metrics={{ ...SERVICE_METRICS_PAYMENTS, tasks_now: null }} />)
    expect(screen.queryByText('Memory')).not.toBeInTheDocument()
    expect(screen.getByText('Zone')).toBeInTheDocument()
  })
})

describe('RunningTasksMeters actions', () => {
  const task = SERVICE_PAYMENTS.running_tasks[0]
  const enabled = {
    ...SERVICE_PAYMENTS,
    enable_execute_command: true,
    running_tasks: SERVICE_PAYMENTS.running_tasks.map((t) => ({ ...t, exec_agent_running: true })),
  }

  it('offers Shell and Logs links and a Stop button per running task', () => {
    renderWithProviders(<RunningTasksMeters service={enabled} metrics={SERVICE_METRICS_PAYMENTS} />)
    const row = screen.getByRole('row', { name: new RegExp(task.short_id) })
    expect(within(row).getByRole('link', { name: 'Shell' })).toHaveAttribute('href', `/services/${enabled.id}/terminal?task=${encodeURIComponent(task.id)}`)
    expect(within(row).getByRole('link', { name: 'Logs' })).toHaveAttribute('href', `/services/${enabled.id}/logs`)
    expect(within(row).getByRole('button', { name: 'Stop' })).toBeEnabled()
  })

  it('Stop opens the confirm dialog instead of stopping straight away', async () => {
    renderWithProviders(<RunningTasksMeters service={enabled} metrics={SERVICE_METRICS_PAYMENTS} />)
    const row = screen.getByRole('row', { name: new RegExp(task.short_id) })
    await userEvent.setup().click(within(row).getByRole('button', { name: 'Stop' }))
    expect(await screen.findByRole('heading', { name: `Stop task ${task.short_id.slice(0, 12)}?` })).toBeInTheDocument()
  })

  it('hides Shell without Terminal or exec, Logs without Logs, and Stop without Operate', () => {
    const limited = { ...enabled, can: { ...enabled.can, terminal: false, logs: false, operate: false } }
    renderWithProviders(<RunningTasksMeters service={limited} metrics={SERVICE_METRICS_PAYMENTS} />)
    expect(screen.queryByRole('link', { name: 'Shell' })).not.toBeInTheDocument()
    expect(screen.queryByRole('link', { name: 'Logs' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Stop' })).not.toBeInTheDocument()
  })

  it('hides Shell when ECS Exec is off or the agent is not running', () => {
    const noExec = { ...enabled, enable_execute_command: false }
    renderWithProviders(<RunningTasksMeters service={noExec} metrics={SERVICE_METRICS_PAYMENTS} />)
    expect(screen.queryByRole('link', { name: 'Shell' })).not.toBeInTheDocument()
  })
})
