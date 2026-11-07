import { QueryClient } from '@tanstack/react-query'
import { fireEvent, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { beforeEach, describe, expect, it } from 'vitest'
import type { AnalyticsCost } from '../../api/analytics'
import { ANALYTICS_COST } from '../../test/fixtures'
import { server } from '../../test/msw'
import { renderApp } from '../../test/renderApp'
import { costSummary, stepAt } from './cost'

beforeEach(() => {
  localStorage.clear()
  window.history.replaceState(null, '', '/')
})

async function renderTab(data: AnalyticsCost = ANALYTICS_COST, queryClient?: QueryClient) {
  server.use(http.get('/api/analytics/cost', () => HttpResponse.json(data)))
  renderApp('/analytics/cost', queryClient)
  return screen.findByRole('region', { name: /Monthly spend/ })
}

const kpi = (name: RegExp) => within(screen.getByRole('list', { name: /Cost key figures/ })).getByText(name).closest('li') as HTMLElement

describe('CostTab', () => {
  it('renders the KPIs and groups by cluster by default', async () => {
    const panel = within(await renderTab())
    expect(kpi(/estimated Fargate spend per month/)).toHaveTextContent('$68')
    expect(kpi(/estimated Fargate spend per month/)).toHaveTextContent('includes $14 of Spot tasks at list price')
    expect(kpi(/possible monthly saving at 30% headroom/)).toHaveTextContent('$25')
    expect(kpi(/possible monthly saving at 30% headroom/)).not.toHaveTextContent('up to')
    expect(kpi(/of spend is reserved but not needed/)).toHaveTextContent('36%')
    expect(kpi(/can use a smaller size/)).toHaveTextContent('1')
    expect(panel.getByText('core-services')).toBeInTheDocument()
    expect(panel.getByText('staging-web')).toBeInTheDocument()
    expect(panel.getByRole('radio', { name: 'Cluster' })).toBeChecked()
  })

  it('switches the grouping to account and tag', async () => {
    const panel = within(await renderTab())
    await userEvent.click(panel.getByRole('radio', { name: 'Account' }))
    expect(panel.getByText(ANALYTICS_COST.by_account[0].label)).toBeInTheDocument()
    expect(panel.queryByText('core-services')).not.toBeInTheDocument()
    await userEvent.click(panel.getByRole('radio', { name: 'Tag' }))
    for (const group of ANALYTICS_COST.by_tag) expect(panel.getByText(group.label)).toBeInTheDocument()
  })

  it('never shows a dollar figure for an unpriced group', async () => {
    const panel = within(await renderTab())
    const row = panel.getByText('eu-apps').closest('li') as HTMLElement
    expect(row).toHaveTextContent('not priced')
    expect(row).not.toHaveTextContent('$')
  })

  it('warns about unpriced tasks and unknown regions', async () => {
    await renderTab()
    expect(screen.getByText('3 tasks are not priced (EC2, other non-Fargate capacity, or a region without a price table).')).toBeInTheDocument()
    expect(screen.getByText('No price table for region ap-south-1. Those tasks are not priced.')).toBeInTheDocument()
  })

  it('shows an em dash for savings when the rollup is off, with spend still visible', async () => {
    await renderTab({ ...ANALYTICS_COST, rightsizing: { ...ANALYTICS_COST.rightsizing, state: 'rollup_off' } })
    expect(kpi(/possible monthly saving/)).toHaveTextContent('—')
    expect(kpi(/reserved but not needed/)).toHaveTextContent('—')
    expect(kpi(/can use a smaller size/)).toHaveTextContent('—')
    expect(kpi(/estimated Fargate spend per month/)).toHaveTextContent('$68')
  })
})

const withKpis = (kpis: Partial<AnalyticsCost['kpis']>): AnalyticsCost => ({ ...ANALYTICS_COST, kpis: { ...ANALYTICS_COST.kpis, ...kpis } })

describe('CostTab edge cases', () => {
  it('says "1 task" and "1 service" in the singular', async () => {
    await renderTab(withKpis({ unpriced_tasks: 1 }))
    expect(screen.getByText('1 task is not priced (EC2, other non-Fargate capacity, or a region without a price table).')).toBeInTheDocument()
    expect(kpi(/can use a smaller size/)).toHaveTextContent(/^service can use a smaller size/)
  })

  it('shows an em dash, not $0, when nothing is priced', async () => {
    await renderTab(withKpis({ monthly_total: 0, spot_monthly: 0, priced_tasks: 0, spot_tasks: 0 }))
    const spend = kpi(/estimated Fargate spend per month/)
    expect(spend).toHaveTextContent('—')
    expect(spend).not.toHaveTextContent('$')
  })

  it('says there is nothing to show when the grouping has no rows', async () => {
    const panel = within(await renderTab({ ...ANALYTICS_COST, by_cluster: [] }))
    expect(panel.getByText('No services to show.')).toBeInTheDocument()
  })

  it('prefixes the saving with "up to" when a Spot service has a suggestion', async () => {
    const [payments, ...rest] = ANALYTICS_COST.rightsizing.services
    const spot = { ...payments, spot_tasks: 1 }
    await renderTab({ ...ANALYTICS_COST, rightsizing: { ...ANALYTICS_COST.rightsizing, services: [spot, ...rest] } })
    expect(kpi(/possible monthly saving/)).toHaveTextContent('up to $25')
  })

  it('caps the not-needed share at 100% when savings exceed spend', async () => {
    await renderTab(withKpis({ monthly_total: 10 }))
    expect(kpi(/reserved but not needed/)).toHaveTextContent('100%')
  })
})

describe('costSummary', () => {
  const [payments] = ANALYTICS_COST.rightsizing.services

  it('flags Spot rows with a suggestion as an upper bound and skips null steps', () => {
    const spot = { ...payments, spot_tasks: 1 }
    expect(costSummary([spot], 30)).toEqual({ saving: 24.6, services: 1, upperBound: true })
    expect(costSummary([spot], 90)).toEqual({ saving: 0, services: 0, upperBound: false })
  })
})

describe('stepAt', () => {
  it('picks the step for the headroom', () => {
    const steps = [
      { from_headroom_pct: 10, suggestion: { cpu_units: 256, memory_mib: 512, saving_monthly: 5, limited_by_containers: false } },
      { from_headroom_pct: 15, suggestion: null },
    ]
    expect(stepAt(steps, 10)).toBe(steps[0].suggestion)
    expect(stepAt(steps, 14)).toBe(steps[0].suggestion)
    expect(stepAt(steps, 15)).toBeNull()
    expect(stepAt(steps, 100)).toBeNull()
  })
})

describe('Rightsizing panel', () => {
  const [payments, catalog, web] = ANALYTICS_COST.rightsizing.services
  const orders = {
    ...payments,
    service_id: 'orders-id',
    service: 'orders-api',
    tasks: 1,
    auto_scaled: false,
    steps: [{ from_headroom_pct: 10, suggestion: { cpu_units: 512, memory_mib: 1024, saving_monthly: 10, limited_by_containers: false } }],
  }
  const withServices = (services: AnalyticsCost['rightsizing']['services'], state: AnalyticsCost['rightsizing']['state'] = 'ok'): AnalyticsCost => ({
    ...ANALYTICS_COST,
    rightsizing: { ...ANALYTICS_COST.rightsizing, state, services },
  })
  const panelOf = async (data?: AnalyticsCost, queryClient?: QueryClient) => {
    await renderTab(data, queryClient)
    return within(screen.getByRole('region', { name: /^Rightsizing/ }))
  }
  const setSlider = (panel: ReturnType<typeof within>, value: number) => fireEvent.change(panel.getByLabelText('Headroom above p95'), { target: { value: String(value) } })

  it('recomputes suggestions and KPIs from the slider without another request', async () => {
    let requests = 0
    server.use(http.get('/api/analytics/cost', () => { requests += 1; return HttpResponse.json(ANALYTICS_COST) }))
    renderApp('/analytics/cost')
    const panel = within(await screen.findByRole('region', { name: /^Rightsizing/ }))
    const row = panel.getByText('payments-api').closest('[role="row"]') as HTMLElement
    expect(row).toHaveTextContent('256 / 1024')
    expect(kpi(/possible monthly saving at 30% headroom/)).toHaveTextContent('$25')
    setSlider(panel, 45)
    expect(panel.getByText('45%')).toBeInTheDocument()
    expect(row).toHaveTextContent('512 / 1024')
    expect(kpi(/possible monthly saving at 45% headroom/)).toHaveTextContent('$19')
    expect(requests).toBe(1)
  })

  it('shows no selection for a service without Edit', async () => {
    const panel = await panelOf(withServices([{ ...orders, can_edit: false, tag: { ...payments.tag, name: 'prod' } }]))
    const box = panel.getByRole('checkbox', { name: 'Select orders-api' })
    expect(box).toBeDisabled()
    expect(box).not.toBeChecked()
    expect(box).toHaveAttribute('title', 'You need Edit on prod')
    expect(panel.getByText('0 selected · $0 per month')).toBeInTheDocument()
    expect(panel.getByRole('button', { name: 'Draft change sets' })).toBeDisabled()
  })

  it('totals only the selected rows', async () => {
    const panel = await panelOf(withServices([payments, orders]))
    expect(panel.getByText('2 selected · $35 per month')).toBeInTheDocument()
    await userEvent.click(panel.getByRole('checkbox', { name: 'Select orders-api' }))
    expect(panel.getByText('1 selected · $25 per month')).toBeInTheDocument()
  })

  it('lists the reason for services with no suggestion', async () => {
    const panel = await panelOf()
    const none = within(screen.getByRole('region', { name: /No suggestion/ }))
    expect(none.getByText('No price for this region')).toBeInTheDocument()
    expect(none.getByText('Some tasks are not on Fargate, so only Fargate tasks are priced')).toBeInTheDocument()
    expect(panel.queryByText('catalog-api')).not.toBeInTheDocument()
    expect(catalog.status).toBe('no_price')
    expect(web.status).toBe('mixed_capacity')
  })

  it('explains the rollup being off', async () => {
    await panelOf(withServices([], 'rollup_off'))
    expect(screen.getByText('The daily utilisation rollup is off, so there are no suggestions. Set HM_ANALYTICS_UTIL=true to collect it.')).toBeInTheDocument()
    expect(screen.queryByRole('region', { name: /No suggestion/ })).not.toBeInTheDocument()
  })

  it('names the retention setting when retention is too short', async () => {
    const panel = await panelOf(withServices([], 'retention_short'))
    expect(panel.getByText(/HM_RETENTION_UTIL_DAILY/)).toHaveTextContent('14 days')
  })

  it('says every service is the right size when nothing has a suggestion', async () => {
    const panel = await panelOf(withServices([{ ...orders, steps: [] }]))
    expect(panel.getByText('At this headroom every service is already the right size.')).toBeInTheDocument()
  })

  it('opens the dialog with one Deploy link per selected service', async () => {
    const posts: string[] = []
    server.use(http.post('*', ({ request }) => { posts.push(request.url); return HttpResponse.json({}) }))
    const panel = await panelOf(withServices([payments, orders]))
    setSlider(panel, 45)
    await userEvent.click(panel.getByRole('button', { name: 'Draft change sets' }))
    const dialog = within(screen.getByRole('dialog', { name: 'Draft 2 change sets' }))
    const links = dialog.getAllByRole('link', { name: 'Open in Deploy' })
    expect(links).toHaveLength(2)
    for (const link of links) {
      expect(link.getAttribute('href')).toContain('task_cpu=')
      expect(link.getAttribute('href')).toContain('task_memory=')
      expect(link.getAttribute('href')).toContain('headroom=45')
      expect(link).toHaveAttribute('target', '_blank')
      expect(link).toHaveAttribute('rel', 'noopener')
    }
    expect(dialog.getByText(/Nothing deploys yet\./)).toHaveTextContent('"Rightsizing at 45% headroom"')
    expect(posts).toEqual([])
  })

  it('marks a service opened after following its link', async () => {
    const panel = await panelOf(withServices([payments]))
    await userEvent.click(panel.getByRole('button', { name: 'Draft change sets' }))
    const dialog = within(screen.getByRole('dialog', { name: 'Draft 1 change set' }))
    expect(dialog.queryByText('Opened')).not.toBeInTheDocument()
    const link = dialog.getByRole('link', { name: 'Open in Deploy' })
    link.addEventListener('click', (event) => event.preventDefault())
    await userEvent.click(link)
    expect(dialog.getByText('Opened')).toBeInTheDocument()
  })

  it('keeps the slider and selection across a refetch', async () => {
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    const panel = await panelOf(withServices([payments, orders]), queryClient)
    setSlider(panel, 45)
    await userEvent.click(panel.getByRole('checkbox', { name: 'Select orders-api' }))
    await queryClient.invalidateQueries({ queryKey: ['analytics'] })
    await waitFor(() => expect(queryClient.isFetching()).toBe(0))
    expect(panel.getByLabelText('Headroom above p95')).toHaveValue('45')
    expect(panel.getByRole('checkbox', { name: 'Select orders-api' })).not.toBeChecked()
    expect(panel.getByRole('checkbox', { name: 'Select payments-api' })).toBeChecked()
  })

  it('marks auto-scaled services', async () => {
    const panel = await panelOf(withServices([payments, orders]))
    const pill = panel.getByText('Auto-scaled')
    expect(pill).toHaveAttribute('title', 'A smaller task may need more tasks, so the saving is an estimate.')
    expect(panel.getAllByText('Auto-scaled')).toHaveLength(1)
  })

  it('marks a suggestion limited by containers and Spot savings as an upper bound', async () => {
    const limited = { ...orders, spot_tasks: 1, steps: [{ from_headroom_pct: 10, suggestion: { cpu_units: 512, memory_mib: 1024, saving_monthly: 10, limited_by_containers: true } }] }
    const panel = await panelOf(withServices([limited]))
    expect(panel.getByText('Limited by containers')).toBeInTheDocument()
    expect(panel.getByText('Spot')).toBeInTheDocument()
    expect(panel.getByText('up to $10')).toBeInTheDocument()
  })
})
