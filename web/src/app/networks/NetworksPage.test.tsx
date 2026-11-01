import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, within } from '@testing-library/react'
import { http, HttpResponse } from 'msw'
import { MemoryRouter, Route, Routes } from 'react-router'
import { describe, expect, it } from 'vitest'
import { AuthProvider } from '../auth/AuthProvider'
import { ToastProvider } from '../../components/Toast'
import { server } from '../../test/msw'
import type { NetworksResponse } from '../../api/networks'
import { NetworksPage } from './NetworksPage'

const NETWORKS: NetworksResponse = {
  polled_at: '2026-11-01T10:00:00Z',
  items: [
    {
      account_id: '482109375512',
      account_name: 'homelab-admin',
      regions: [
        { region: 'us-east-1', active_vpcs: 2, hidden_vpcs: 1, clusters: 3, tasks: 12, unplaced_tasks: 0 },
        { region: 'eu-west-1', active_vpcs: 0, hidden_vpcs: 0, clusters: 0, tasks: 0, unplaced_tasks: 0 },
      ],
    },
  ],
}

function renderNetworksPage(route = '/networks') {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter initialEntries={[route]}>
        <ToastProvider>
          <AuthProvider>
            <Routes>
              <Route path="/networks" element={<NetworksPage />} />
            </Routes>
          </AuthProvider>
        </ToastProvider>
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

/** Matches a stat span by its full reading ("2 active VPCs"), since the number is in its own <b>. */
const statText = (text: string) => (_: string, element: Element | null) =>
  element?.tagName === 'SPAN' && element.textContent === text

/** Serves one account whose only region is `region`. */
function serveRegion(region: NetworksResponse['items'][number]['regions'][number]) {
  server.use(
    http.get('/api/networks', () =>
      HttpResponse.json({ ...NETWORKS, items: [{ ...NETWORKS.items[0], regions: [region] }] }),
    ),
  )
}

describe('NetworksPage', () => {
  it('shows each account with its name and account number', async () => {
    server.use(http.get('/api/networks', () => HttpResponse.json(NETWORKS)))
    renderNetworksPage()
    expect(await screen.findByText('homelab-admin')).toBeInTheDocument()
    expect(screen.getByText('482109375512')).toBeInTheDocument()
  })

  it('renders an active region card with its stats, hidden-VPC note and link', async () => {
    server.use(http.get('/api/networks', () => HttpResponse.json(NETWORKS)))
    renderNetworksPage()
    const card = await screen.findByRole('link', { name: /us-east-1/ })
    expect(card).toHaveAttribute('href', '/networks/482109375512/us-east-1')
    expect(within(card).getByText(statText('2 active VPCs'))).toBeInTheDocument()
    expect(within(card).getByText(statText('3 clusters'))).toBeInTheDocument()
    expect(within(card).getByText(statText('12 tasks'))).toBeInTheDocument()
    expect(within(card).getByText('1 VPC without tasks hidden')).toBeInTheDocument()
  })

  it('pluralises the hidden-VPC note when more than one VPC is hidden', async () => {
    serveRegion({ region: 'us-east-1', active_vpcs: 1, hidden_vpcs: 2, clusters: 1, tasks: 3, unplaced_tasks: 0 })
    renderNetworksPage()
    expect(await screen.findByText('2 VPCs without tasks hidden')).toBeInTheDocument()
  })

  it('renders a region without active VPCs as dimmed text with no link', async () => {
    server.use(http.get('/api/networks', () => HttpResponse.json(NETWORKS)))
    renderNetworksPage()
    const dimmedText = await screen.findByText('No active VPCs')
    expect(dimmedText.closest('a')).toBeNull()
  })

  it('keeps the unplaced-tasks note on a region without active VPCs, with no link', async () => {
    serveRegion({ region: 'eu-west-1', active_vpcs: 0, hidden_vpcs: 0, clusters: 0, tasks: 0, unplaced_tasks: 2 })
    renderNetworksPage()
    const dimmedText = await screen.findByText('No active VPCs')
    expect(screen.getByText('2 tasks without a subnet not shown')).toBeInTheDocument()
    expect(dimmedText.closest('a')).toBeNull()
  })

  it('uses the singular unplaced-tasks note for one task', async () => {
    serveRegion({ region: 'us-east-1', active_vpcs: 1, hidden_vpcs: 0, clusters: 1, tasks: 1, unplaced_tasks: 1 })
    renderNetworksPage()
    expect(await screen.findByText('1 task without a subnet not shown')).toBeInTheDocument()
  })
})
