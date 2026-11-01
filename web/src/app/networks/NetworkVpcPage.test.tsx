import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { MemoryRouter, Route, Routes } from 'react-router'
import { describe, expect, it } from 'vitest'
import { AuthProvider } from '../auth/AuthProvider'
import { ToastProvider } from '../../components/Toast'
import { server } from '../../test/msw'
import { renderApp } from '../../test/renderApp'
import type { VpcTreeResponse } from '../../api/networks'
import { buildTree } from './tree/treeFixture'
import { NetworkVpcPage } from './NetworkVpcPage'

const VPC_ROUTE = '/networks/482109375512/us-east-1/vpc-0a41c7e29b3d'

function treeResponse(overrides: Partial<VpcTreeResponse> = {}): VpcTreeResponse {
  return {
    polled_at: '2026-11-01T10:00:00Z',
    vpc: {
      vpc_id: 'vpc-0a41c7e29b3d',
      name: 'homelab-main',
      cidr: '10.20.0.0/16',
      account_id: '482109375512',
      account_name: 'homelab-admin',
      region: 'us-east-1',
    },
    edges: [
      { kind: 'igw', id: 'igw-1' },
      { kind: 'nat', id: 'nat-1', subnet_id: 'subnet-aaa' },
      { kind: 'endpoint', id: 'vpce-1', detail: 'logs' },
      { kind: 'endpoint', id: 'vpce-2', detail: 'ecr.dkr' },
      { kind: 'endpoint', id: 'vpce-3', detail: 'ssmmessages' },
      { kind: 'endpoint', id: 'vpce-4', detail: 'ecr.api' },
    ],
    summary: { subnets: 3, zones: 2, running_tasks: 10, unplaced_tasks: 0, collapsed_by_default: false },
    root: buildTree(),
    ...overrides,
  }
}

function renderVpcPage(route = VPC_ROUTE, queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })) {
  return render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter initialEntries={[route]}>
        <ToastProvider>
          <AuthProvider>
            <Routes>
              <Route path="/networks/:accountId/:region/:vpcId" element={<NetworkVpcPage />} />
            </Routes>
          </AuthProvider>
        </ToastProvider>
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

const serveTree = (body: VpcTreeResponse) => server.use(http.get('/api/vpcs/:vpcId/tree', () => HttpResponse.json(body)))

describe('NetworkVpcPage', () => {
  it('renders header meta and edge summary', async () => {
    serveTree(treeResponse())
    renderVpcPage()
    expect(await screen.findByRole('heading', { name: 'homelab-main' })).toBeInTheDocument()
    expect(screen.getByText('vpc-0a41c7e29b3d')).toBeInTheDocument()
    expect(screen.getByText('10.20.0.0/16')).toBeInTheDocument()
    expect(screen.getByText('3 subnets')).toBeInTheDocument()
    expect(screen.getByText('10 running tasks')).toBeInTheDocument()
    expect(screen.getByText('Internet gateway')).toBeInTheDocument()
    expect(screen.getByText('NAT gateway')).toBeInTheDocument()
    expect(screen.getByText('Endpoints: ecr.api, ecr.dkr, logs, ssmmessages')).toBeInTheDocument()
  })

  it('says "1 running task" for a single task', async () => {
    serveTree(treeResponse({ summary: { subnets: 1, zones: 1, running_tasks: 1, unplaced_tasks: 0, collapsed_by_default: false } }))
    renderVpcPage()
    expect(await screen.findByText('1 running task')).toBeInTheDocument()
    expect(screen.queryByText('1 running tasks')).not.toBeInTheDocument()
  })

  it('Collapse to services hides task nodes, Expand all restores them', async () => {
    serveTree(treeResponse())
    renderVpcPage()
    const user = userEvent.setup()
    const tree = await screen.findByRole('tree', { name: 'Network tree' })
    expect(within(tree).getByRole('treeitem', { name: /11111111/ })).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: 'Collapse to services' }))
    expect(within(tree).getByRole('treeitem', { name: /payments-api/ })).toBeInTheDocument()
    expect(within(tree).queryByRole('treeitem', { name: /11111111/ })).not.toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: 'Expand all' }))
    expect(within(tree).getByRole('treeitem', { name: /11111111/ })).toBeInTheDocument()
  })

  it('starts collapsed when collapsed_by_default', async () => {
    serveTree(treeResponse({ summary: { subnets: 3, zones: 2, running_tasks: 10, unplaced_tasks: 0, collapsed_by_default: true } }))
    renderVpcPage()
    const tree = await screen.findByRole('tree', { name: 'Network tree' })
    expect(within(tree).getByRole('treeitem', { name: /payments-api/ })).toBeInTheDocument()
    expect(within(tree).queryByRole('treeitem', { name: /11111111/ })).not.toBeInTheDocument()
  })

  it('shows the empty state with a link back to the region on 404', async () => {
    server.use(
      http.get('/api/vpcs/:vpcId/tree', () =>
        HttpResponse.json({ error: { code: 'not_found', message: 'Not found.' } }, { status: 404 }),
      ),
    )
    renderVpcPage()
    expect(await screen.findByText('No running tasks you can see in this VPC.')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Back to us-east-1' })).toHaveAttribute('href', '/networks/482109375512/us-east-1')
  })

  it('hides the header meta and actions when a refetch returns 404 after a load', async () => {
    serveTree(treeResponse())
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    renderVpcPage(VPC_ROUTE, queryClient)
    expect(await screen.findByText('10 running tasks')).toBeInTheDocument()

    server.use(
      http.get('/api/vpcs/:vpcId/tree', () =>
        HttpResponse.json({ error: { code: 'not_found', message: 'Not found.' } }, { status: 404 }),
      ),
    )
    await queryClient.invalidateQueries({ queryKey: ['vpcTree'] })
    expect(await screen.findByText('No running tasks you can see in this VPC.')).toBeInTheDocument()
    expect(screen.queryByText('10 running tasks')).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Expand all' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Collapse to services' })).not.toBeInTheDocument()
  })

  it('routes resolve: the VPC URL renders the page inside the app shell', async () => {
    serveTree(treeResponse())
    renderApp(VPC_ROUTE)
    expect(await screen.findByRole('heading', { name: 'homelab-main' })).toBeInTheDocument()
    expect(screen.queryByText('Page not found.')).not.toBeInTheDocument()
  })
})
