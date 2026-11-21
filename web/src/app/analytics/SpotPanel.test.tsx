import { QueryClient } from '@tanstack/react-query'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import type { AnalyticsCost, CostSpotService } from '../../api/analytics'
import { ANALYTICS_COST } from '../../test/fixtures'
import { server } from '../../test/msw'
import { installPhoneMedia } from '../../test/phone'
import { renderApp } from '../../test/renderApp'

beforeEach(() => {
  localStorage.clear()
  window.history.replaceState(null, '', '/')
})

const [, catalog, , payments, web] = ANALYTICS_COST.spot.services
const CLUSTER_COPY =
  'Cluster edge-proxy has no FARGATE_SPOT capacity provider. Associate it with the cluster (in Terraform: aws_ecs_cluster_capacity_providers), then this service qualifies.'

const withSpot = (services: CostSpotService[]): AnalyticsCost => ({ ...ANALYTICS_COST, spot: { ...ANALYTICS_COST.spot, services } })

async function spotPanel(data: AnalyticsCost = ANALYTICS_COST, queryClient?: QueryClient) {
  server.use(http.get('/api/analytics/cost', () => HttpResponse.json(data)))
  renderApp('/analytics/cost', queryClient)
  return within(await screen.findByRole('region', { name: /^Fargate Spot/ }))
}

const kpi = (name: RegExp) => within(screen.getByRole('list', { name: /Cost key figures/ })).getByText(name).closest('li') as HTMLElement
const rowOf = (panel: ReturnType<typeof within>, name: string) => panel.getByRole('link', { name }).closest('[role="row"]') as HTMLElement

describe('Fargate Spot panel', () => {
  it('ranks Spot candidates by saving and shows why each qualifies', async () => {
    const panel = await spotPanel()
    expect(screen.getByRole('region', { name: /^Fargate Spot/ })).toHaveTextContent('candidates by monthly saving · Spot prices as of 21 Nov 2026')
    const table = within(panel.getByRole('table', { name: 'Fargate Spot candidates' }))
    const names = table.getAllByRole('link').map((link) => link.textContent)
    expect(names).toEqual(['report-worker', 'catalog-api'])

    const row = within(rowOf(panel, 'catalog-api'))
    expect(row.getByText('2 now · 1 on-demand, 1 on Spot')).toBeInTheDocument()
    expect(row.getByText('Non-production tag Dev')).toBeInTheDocument()
    expect(row.getByText('2 tasks; 1 stays on-demand')).toBeInTheDocument()
    expect(row.getByText('No terminal sessions in 7 days')).toBeInTheDocument()
    expect(row.getByText('Stops cleanly on SIGTERM (4 stops in 7 days)')).toBeInTheDocument()
    expect(row.getByText('about $13')).toBeInTheDocument()
    expect(row.getByText('Auto-scaled')).toBeInTheDocument()
  })

  it('says when there is nothing to judge SIGTERM handling by', async () => {
    const panel = await spotPanel(withSpot([{ ...catalog, expected_stops: 0, stop_evidence: 'unknown' }]))
    const row = within(rowOf(panel, 'catalog-api'))
    expect(row.getByText('No deployment or scale-in stops in 7 days to judge SIGTERM handling')).toBeInTheDocument()
    expect(row.queryByText('Check first')).not.toBeInTheDocument()
    expect(row.getByRole('checkbox', { name: 'Select catalog-api' })).toBeChecked()
  })

  it('leaves a candidate with a caution unticked', async () => {
    const panel = await spotPanel()
    const row = within(rowOf(panel, 'report-worker'))
    expect(row.getByText('Check first')).toBeInTheDocument()
    expect(row.getByRole('checkbox', { name: 'Select report-worker' })).not.toBeChecked()
    expect(row.getByText('2 terminal sessions in 7 days')).toBeInTheDocument()
    expect(row.getByText('1 of 3 stops in 7 days were killed (exit 137)')).toBeInTheDocument()
    expect(panel.getByText('1 selected · about $13 per month')).toBeInTheDocument()
    expect(kpi(/possible monthly saving on Fargate Spot/)).toHaveTextContent('about $13')

    await userEvent.click(row.getByRole('checkbox', { name: 'Select report-worker' }))
    expect(panel.getByText('2 selected · about $54 per month')).toBeInTheDocument()
  })

  it('cannot tick a candidate without Deploy', async () => {
    const panel = await spotPanel(withSpot([{ ...catalog, can_deploy: false }]))
    const box = panel.getByRole('checkbox', { name: 'Select catalog-api' })
    expect(box).toBeDisabled()
    expect(box).not.toBeChecked()
    expect(box).toHaveAttribute('title', 'You need Deploy on Dev')
    expect(panel.getByText('0 selected · about $0 per month')).toBeInTheDocument()
    expect(panel.getByRole('button', { name: 'Draft change sets' })).toBeDisabled()
  })

  it('drafts Spot change sets with a Deploy link per service', async () => {
    const posts: string[] = []
    server.use(http.post('*', ({ request }) => { posts.push(request.url); return HttpResponse.json({}) }))
    const panel = await spotPanel()
    await userEvent.click(panel.getByRole('checkbox', { name: 'Select report-worker' }))
    await userEvent.click(panel.getByRole('button', { name: 'Draft change sets' }))

    const dialog = within(screen.getByRole('dialog', { name: 'Draft 2 change sets' }))
    const links = dialog.getAllByRole('link', { name: 'Open in Deploy' })
    expect(links).toHaveLength(2)
    for (const link of links) {
      expect(link.getAttribute('href')).toContain('capacity=spot')
      expect(link.getAttribute('href')).toContain('on_demand_base=1')
      expect(link).toHaveAttribute('target', '_blank')
      expect(link).toHaveAttribute('rel', 'noopener')
    }
    expect(dialog.getAllByText('Now FARGATE')).toHaveLength(2)
    expect(dialog.getAllByText('Suggested FARGATE base 1, FARGATE_SPOT weight 1')).toHaveLength(2)
    expect(dialog.getByText(/Nothing deploys yet\./)).toHaveTextContent(
      `Nothing deploys yet. Each link opens that service's Deploy tab with the capacity providers filled in and the reason "Move to Fargate Spot, 1 on-demand". Nothing is saved until you review and apply it there.`,
    )
    expect(posts).toEqual([])
  })

  it('explains a cluster without FARGATE_SPOT', async () => {
    await spotPanel()
    const notCandidates = within(screen.getByRole('region', { name: /Not candidates/ }))
    const items = notCandidates.getAllByRole('listitem')
    expect(items[0]).toHaveTextContent('edge-nginx')
    expect(items[0]).toHaveTextContent(CLUSTER_COPY)
    expect(items[0]).toHaveTextContent('about $7 per month')
  })

  it('lists non-candidates with their reason', async () => {
    await spotPanel()
    const notCandidates = within(screen.getByRole('region', { name: /Not candidates/ }))
    const item = (name: string) => notCandidates.getByRole('link', { name }).closest('li') as HTMLElement
    expect(item('payments-api')).toHaveTextContent('Production tag Prod')
    expect(item('web-frontend')).toHaveTextContent('Already uses Fargate Spot')
  })

  it.each([
    ['single_task', 'Runs 1 task; Spot needs at least 2'],
    ['not_fargate', 'Not on Fargate'],
    ['windows', 'Windows tasks cannot run on Fargate Spot'],
    ['no_price', 'No Spot price for this region'],
    ['not_running', 'No running on-demand tasks'],
    ['custom_capacity', 'Uses a capacity provider Harbormaster does not edit'],
  ] as const)('explains the %s status', async (status, text) => {
    await spotPanel(withSpot([{ ...payments, status }]))
    expect(within(screen.getByRole('region', { name: /Not candidates/ })).getByText(text)).toBeInTheDocument()
  })

  it('names the controller Harbormaster does not deploy', async () => {
    await spotPanel(withSpot([{ ...payments, status: 'unsupported_controller', controller_name: 'CodeDeploy' }]))
    expect(within(screen.getByRole('region', { name: /Not candidates/ })).getByText('Deployment controller CodeDeploy is not supported')).toBeInTheDocument()
  })

  it('says no service qualifies and which tags are non-production', async () => {
    const panel = await spotPanel(withSpot([payments, { ...web, tag: { ...web.tag, non_production: true } }]))
    expect(panel.getByText('No service qualifies for Fargate Spot.')).toBeInTheDocument()
    expect(panel.getByText('Non-production tags: Staging.')).toBeInTheDocument()
    expect(panel.queryByRole('button', { name: 'Draft change sets' })).not.toBeInTheDocument()
    expect(kpi(/possible monthly saving on Fargate Spot/)).toHaveTextContent('—')
  })

  it('tells the admin where to mark a tag when none is non-production', async () => {
    const panel = await spotPanel(withSpot([payments]))
    expect(panel.getByText('Mark a tag as non-production in Admin, Tags to get suggestions.')).toBeInTheDocument()
  })

  it('keeps the selections of all three advisors across a refetch', async () => {
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    const spot = await spotPanel(ANALYTICS_COST, queryClient)
    const rightsizing = within(screen.getByRole('region', { name: /^Rightsizing/ }))
    await userEvent.click(rightsizing.getByRole('checkbox', { name: 'Select payments-api' }))
    await userEvent.click(spot.getByRole('checkbox', { name: 'Select report-worker' }))
    await userEvent.click(spot.getByRole('checkbox', { name: 'Select catalog-api' }))

    await queryClient.invalidateQueries({ queryKey: ['analytics'] })
    await waitFor(() => expect(queryClient.isFetching()).toBe(0))

    expect(rightsizing.getByRole('checkbox', { name: 'Select payments-api' })).not.toBeChecked()
    expect(spot.getByRole('checkbox', { name: 'Select report-worker' })).toBeChecked()
    expect(spot.getByRole('checkbox', { name: 'Select catalog-api' })).not.toBeChecked()
  })

  it('does not add the savings of different advisors together', async () => {
    await spotPanel()
    const labels = within(screen.getByRole('list', { name: /Cost key figures/ })).getAllByRole('listitem').map((item) => item.textContent)
    expect(labels.some((text) => /total saving|all advisors/i.test(text ?? ''))).toBe(false)
  })
})

describe('rightsizing draft dialog', () => {
  it('reads as before', async () => {
    renderApp('/analytics/cost')
    const rightsizing = within(await screen.findByRole('region', { name: /^Rightsizing/ }))
    await userEvent.click(rightsizing.getByRole('button', { name: 'Draft change sets' }))
    const dialog = within(screen.getByRole('dialog', { name: 'Draft 1 change set' }))
    expect(dialog.getByText('Now 1024 / 2048')).toBeInTheDocument()
    expect(dialog.getByText('Suggested 256 / 1024')).toBeInTheDocument()
    expect(dialog.getByText(/Nothing deploys yet\./)).toHaveTextContent(
      `Nothing deploys yet. Each link opens that service's Deploy tab with the size filled in and the reason "Rightsizing at 30% headroom". Nothing is saved until you review and apply it there.`,
    )
  })
})

describe('Fargate Spot panel on a phone', () => {
  let restoreMedia: () => void
  beforeEach(() => {
    restoreMedia = installPhoneMedia(true)
  })
  afterEach(() => restoreMedia())

  it('renders Spot candidates as cards', async () => {
    const panel = await spotPanel()
    const row = within(rowOf(panel, 'catalog-api'))
    expect(row.getByText('about $13')).toBeInTheDocument()
    expect(row.getByText('Non-production tag Dev')).toBeInTheDocument()
    expect(row.getByRole('checkbox', { name: 'Select catalog-api' })).toBeInTheDocument()
    expect(panel.queryByRole('columnheader')).not.toBeInTheDocument()
  })
})
