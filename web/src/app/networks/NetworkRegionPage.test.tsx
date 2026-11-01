import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, within } from '@testing-library/react'
import { http, HttpResponse } from 'msw'
import { MemoryRouter, Route, Routes } from 'react-router'
import { describe, expect, it } from 'vitest'
import { AuthProvider } from '../auth/AuthProvider'
import { ToastProvider } from '../../components/Toast'
import { server } from '../../test/msw'
import type { NetworksResponse, RegionVpcsResponse } from '../../api/networks'
import { NetworkRegionPage } from './NetworkRegionPage'

const ACCOUNTS: NetworksResponse = {
  polled_at: '2026-11-01T10:00:00Z',
  items: [{ account_id: '482109375512', account_name: 'homelab-admin', regions: [] }],
}

const REGION: RegionVpcsResponse = {
  polled_at: '2026-11-01T10:00:00Z',
  items: [
    {
      vpc_id: 'vpc-0a41c7e29b3d',
      name: 'homelab-main',
      cidr: '10.20.0.0/16',
      subnets: 3,
      clusters: ['core-services', 'background-jobs'],
      tasks: 10,
    },
  ],
}

function renderRegionPage(route = '/networks/482109375512/us-east-1') {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter initialEntries={[route]}>
        <ToastProvider>
          <AuthProvider>
            <Routes>
              <Route path="/networks/:accountId/:region" element={<NetworkRegionPage />} />
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

describe('NetworkRegionPage', () => {
  it('shows the region heading and the account name it belongs to', async () => {
    server.use(
      http.get('/api/networks', () => HttpResponse.json(ACCOUNTS)),
      http.get('/api/networks/:accountId/:region', () => HttpResponse.json(REGION)),
    )
    renderRegionPage()
    expect(await screen.findByRole('heading', { name: 'us-east-1' })).toBeInTheDocument()
    expect(screen.getByText('Active VPCs in homelab-admin')).toBeInTheDocument()
  })

  it('renders each active VPC with its id, CIDR, subnets and cluster names, linking to the VPC', async () => {
    server.use(
      http.get('/api/networks', () => HttpResponse.json(ACCOUNTS)),
      http.get('/api/networks/:accountId/:region', () => HttpResponse.json(REGION)),
    )
    renderRegionPage()
    const card = await screen.findByRole('link', { name: /homelab-main/ })
    expect(card).toHaveAttribute('href', '/networks/482109375512/us-east-1/vpc-0a41c7e29b3d')
    expect(within(card).getByText('vpc-0a41c7e29b3d · 10.20.0.0/16')).toBeInTheDocument()
    expect(within(card).getByText(statText('3 subnets'))).toBeInTheDocument()
    expect(within(card).getByText('core-services, background-jobs')).toBeInTheDocument()
  })

  it('shows an empty state when the region has no active VPCs', async () => {
    server.use(
      http.get('/api/networks', () => HttpResponse.json(ACCOUNTS)),
      http.get('/api/networks/:accountId/:region', () => HttpResponse.json({ ...REGION, items: [] })),
    )
    renderRegionPage()
    expect(await screen.findByText('No active VPCs in this region.')).toBeInTheDocument()
  })
})
