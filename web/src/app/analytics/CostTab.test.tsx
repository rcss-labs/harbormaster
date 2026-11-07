import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { beforeEach, describe, expect, it } from 'vitest'
import type { AnalyticsCost } from '../../api/analytics'
import { ANALYTICS_COST } from '../../test/fixtures'
import { server } from '../../test/msw'
import { renderApp } from '../../test/renderApp'
import { stepAt } from './cost'

beforeEach(() => {
  localStorage.clear()
  window.history.replaceState(null, '', '/')
})

async function renderTab(data: AnalyticsCost = ANALYTICS_COST) {
  server.use(http.get('/api/analytics/cost', () => HttpResponse.json(data)))
  renderApp('/analytics/cost')
  return screen.findByRole('region', { name: /Monthly spend/ })
}

const kpi = (name: RegExp) => within(screen.getByRole('list', { name: /Cost key figures/ })).getByText(name).closest('li') as HTMLElement

describe('CostTab', () => {
  it('renders the KPIs and groups by cluster by default', async () => {
    const panel = within(await renderTab())
    expect(kpi(/estimated Fargate spend per month/)).toHaveTextContent('$61')
    expect(kpi(/estimated Fargate spend per month/)).toHaveTextContent('includes $10 of Spot tasks at list price')
    expect(kpi(/possible monthly saving at 30% headroom/)).toHaveTextContent('$25')
    expect(kpi(/possible monthly saving at 30% headroom/)).not.toHaveTextContent('up to')
    expect(kpi(/of spend is reserved but not needed/)).toHaveTextContent('40%')
    expect(kpi(/services can use a smaller size/)).toHaveTextContent('1')
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
    expect(screen.getByText('2 tasks are not priced (EC2 or other non-Fargate capacity).')).toBeInTheDocument()
    expect(screen.getByText('No price table for region ap-south-1. Those tasks are not priced.')).toBeInTheDocument()
  })

  it('shows an em dash for savings when the rollup is off, with spend still visible', async () => {
    await renderTab({ ...ANALYTICS_COST, rightsizing: { ...ANALYTICS_COST.rightsizing, state: 'rollup_off' } })
    expect(kpi(/possible monthly saving/)).toHaveTextContent('—')
    expect(kpi(/reserved but not needed/)).toHaveTextContent('—')
    expect(kpi(/can use a smaller size/)).toHaveTextContent('—')
    expect(kpi(/estimated Fargate spend per month/)).toHaveTextContent('$61')
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
