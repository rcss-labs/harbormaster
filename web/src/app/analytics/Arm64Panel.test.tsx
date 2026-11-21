import { QueryClient } from '@tanstack/react-query'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import type { AnalyticsCost, Arm64Service } from '../../api/analytics'
import { ANALYTICS_COST } from '../../test/fixtures'
import { server } from '../../test/msw'
import { installPhoneMedia } from '../../test/phone'
import { renderApp } from '../../test/renderApp'

beforeEach(() => {
  localStorage.clear()
  window.history.replaceState(null, '', '/')
})

const [ready, , unknown] = ANALYTICS_COST.arm64.services

const withArm64 = (services: Arm64Service[], patch: Partial<AnalyticsCost['arm64']> = {}): AnalyticsCost => ({
  ...ANALYTICS_COST,
  arm64: { ...ANALYTICS_COST.arm64, services, ...patch },
})

async function armPanel(data: AnalyticsCost = ANALYTICS_COST, queryClient?: QueryClient) {
  server.use(http.get('/api/analytics/cost', () => HttpResponse.json(data)))
  renderApp('/analytics/cost', queryClient)
  return within(await screen.findByRole('region', { name: /^ARM64 \(Graviton\)/ }))
}

const rowOf = (panel: ReturnType<typeof within>, name: string) => panel.getByRole('link', { name }).closest('[role="row"]') as HTMLElement
const kpi = (name: RegExp) => within(screen.getByRole('list', { name: /Cost key figures/ })).getByText(name).closest('li') as HTMLElement

describe('ARM64 panel', () => {
  it('lists ready services by saving', async () => {
    const panel = await armPanel()
    expect(screen.getByRole('region', { name: /^ARM64 \(Graviton\)/ })).toHaveTextContent('x86 Fargate services by monthly saving at list price')
    const names = within(panel.getByRole('table', { name: 'ARM64 suggestions' })).getAllByRole('link').map((link) => link.textContent)
    expect(names).toEqual(['payments-api', 'catalog-api', 'edge-nginx']) // ready first, then by saving
    const row = within(rowOf(panel, 'payments-api'))
    expect(row.getByText('2 of 2 images have an arm64 build')).toBeInTheDocument()
    expect(row.getByText('$33')).toBeInTheDocument()
    expect(row.getByRole('checkbox', { name: 'Select payments-api' })).toBeChecked()
    expect(panel.getByText('1 selected · $33 per month')).toBeInTheDocument()
    expect(kpi(/possible monthly saving on ARM64/)).toHaveTextContent('$33')
  })

  it('says a saving that includes Spot tasks is about', async () => {
    const panel = await armPanel(withArm64([{ ...ready, spot_tasks: 1 }]))
    expect(within(rowOf(panel, 'payments-api')).getByText('about $33')).toBeInTheDocument()
  })

  it('lists the blocking image', async () => {
    const panel = await armPanel()
    const row = within(rowOf(panel, 'edge-nginx'))
    expect(row.getByText('0 of 1 images have an arm64 build')).toBeInTheDocument()
    expect(row.getByText('482109375512.dkr.ecr.eu-central-1.amazonaws.com/edge-nginx:1.27.2 is a single-architecture image')).toBeInTheDocument()
    expect(row.getByText('Blocked')).toBeInTheDocument()
    expect(row.getByRole('checkbox', { name: 'Select edge-nginx' })).toBeDisabled()
    expect(row.getByRole('checkbox', { name: 'Select edge-nginx' })).not.toBeChecked()
    expect(row.getByText('about $4')).toBeInTheDocument() // the saving at stake, still shown
  })

  it('shows unchecked images as not checked, never as ready', async () => {
    const panel = await armPanel(withArm64([{ ...unknown, images: [{ ...unknown.images[0], reason: 'pending', account: '' }] }]))
    const row = within(rowOf(panel, 'catalog-api'))
    expect(row.getByText('Not checked')).toBeInTheDocument()
    expect(row.getByText(/has not been checked yet$/)).toBeInTheDocument()
    expect(row.queryByText('Blocked')).not.toBeInTheDocument()
    expect(row.getByRole('checkbox', { name: 'Select catalog-api' })).not.toBeChecked()
    expect(row.getByRole('checkbox', { name: 'Select catalog-api' })).toBeDisabled()
  })

  it('shows the per-account permission message', async () => {
    const panel = await armPanel()
    expect(within(rowOf(panel, 'catalog-api')).getByText('The harbormaster-access role in homelab-admin cannot read image manifests. Update the access module to the latest version.')).toBeInTheDocument()
  })

  it('drafts ARM64 change sets with a Deploy link per service', async () => {
    const posts: string[] = []
    server.use(http.post('*', ({ request }) => { posts.push(request.url); return HttpResponse.json({}) }))
    const panel = await armPanel()
    await userEvent.click(panel.getByRole('button', { name: 'Draft change sets' }))
    const dialog = within(screen.getByRole('dialog', { name: 'Draft 1 change set' }))
    const link = dialog.getByRole('link', { name: 'Open in Deploy' })
    expect(link.getAttribute('href')).toContain('arch=ARM64')
    expect(link).toHaveAttribute('target', '_blank')
    expect(link).toHaveAttribute('rel', 'noopener')
    expect(dialog.getByText('Now X86_64')).toBeInTheDocument()
    expect(dialog.getByText('Suggested ARM64')).toBeInTheDocument()
    expect(dialog.getByText(/Nothing deploys yet\./)).toHaveTextContent(
      `Nothing deploys yet. Each link opens that service's Deploy tab with the CPU architecture filled in and the reason "Move to ARM64 (Graviton)". Nothing is saved until you review and apply it there.`,
    )
    expect(posts).toEqual([])
  })

  it('cannot tick a service without Edit', async () => {
    const panel = await armPanel(withArm64([{ ...ready, can_edit: false }]))
    const box = panel.getByRole('checkbox', { name: 'Select payments-api' })
    expect(box).toBeDisabled()
    expect(box).not.toBeChecked()
    expect(box).toHaveAttribute('title', 'You need Edit on Prod')
    expect(panel.getByText('0 selected · $0 per month')).toBeInTheDocument()
  })

  it('shows the off state', async () => {
    const panel = await armPanel(withArm64([], { check_enabled: false }))
    expect(panel.getByText('The image check is off, so there are no ARM64 suggestions. Set HM_ARM64_CHECK=true to turn it on.')).toBeInTheDocument()
  })

  it('says when there is nothing to move', async () => {
    const panel = await armPanel(withArm64([]))
    expect(panel.getByText('Every Fargate service already runs on ARM64, or none runs on x86 Fargate.')).toBeInTheDocument()
  })

  it('says savings do not add up', async () => {
    const panel = await armPanel()
    expect(panel.getByText('Savings are per advisor and do not add up: each is measured against how the service runs today.')).toBeInTheDocument()
  })

  it('keeps the selections of all three advisors across a refetch', async () => {
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    const arm = await armPanel(ANALYTICS_COST, queryClient)
    const rightsizing = within(screen.getByRole('region', { name: /^Rightsizing/ }))
    const spot = within(screen.getByRole('region', { name: /^Fargate Spot/ }))
    await userEvent.click(rightsizing.getByRole('checkbox', { name: 'Select payments-api' }))
    await userEvent.click(spot.getByRole('checkbox', { name: 'Select catalog-api' }))
    await userEvent.click(arm.getByRole('checkbox', { name: 'Select payments-api' }))

    await queryClient.invalidateQueries({ queryKey: ['analytics'] })
    await waitFor(() => expect(queryClient.isFetching()).toBe(0))

    expect(rightsizing.getByRole('checkbox', { name: 'Select payments-api' })).not.toBeChecked()
    expect(spot.getByRole('checkbox', { name: 'Select catalog-api' })).not.toBeChecked()
    expect(arm.getByRole('checkbox', { name: 'Select payments-api' })).not.toBeChecked()
  })
})

describe('ARM64 panel on a phone', () => {
  let restoreMedia: () => void
  beforeEach(() => {
    restoreMedia = installPhoneMedia(true)
  })
  afterEach(() => restoreMedia())

  it('renders ARM64 rows as cards', async () => {
    const panel = await armPanel()
    const card = within(rowOf(panel, 'edge-nginx'))
    expect(card.getByText('Blocked')).toBeInTheDocument()
    expect(card.getByText('about $4')).toBeInTheDocument()
    expect(panel.queryByRole('columnheader')).not.toBeInTheDocument()
  })
})
