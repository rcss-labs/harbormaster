import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import type { NetworksResponse, RegionVpcsResponse, VpcTreeResponse } from '../../api/networks'
import { server } from '../../test/msw'
import { renderApp } from '../../test/renderApp'
import { buildTree } from './treeFixture'

const HOMELAB = '482109375512'
const STAGING = '731022849906'
const MAIN = 'vpc-0a41c7e29b3d'
const SHARED = 'vpc-0b52d8f3ac4e'
const STAGING_VPC = 'vpc-0c63e9a4bd5f'
const SHARED_KEY = `${HOMELAB}/us-east-1/${SHARED}`

const NETWORKS: NetworksResponse = {
  polled_at: '2026-11-01T10:00:00Z',
  items: [
    {
      account_id: HOMELAB,
      account_name: 'homelab-admin',
      regions: [
        { region: 'us-east-1', active_vpcs: 2, hidden_vpcs: 1, clusters: 3, tasks: 12, unplaced_tasks: 0 },
        { region: 'eu-west-1', active_vpcs: 0, hidden_vpcs: 0, clusters: 0, tasks: 0, unplaced_tasks: 0 },
      ],
    },
    {
      account_id: STAGING,
      account_name: 'staging-example',
      regions: [{ region: 'eu-central-1', active_vpcs: 1, hidden_vpcs: 0, clusters: 1, tasks: 2, unplaced_tasks: 0 }],
    },
  ],
}

const REGION_VPCS: Record<string, RegionVpcsResponse> = {
  [`${HOMELAB}/us-east-1`]: {
    polled_at: NETWORKS.polled_at,
    items: [
      { vpc_id: MAIN, name: 'homelab-main', cidr: '10.20.0.0/16', subnets: 3, clusters: ['core-services', 'ci-tools'], tasks: 10 },
      { vpc_id: SHARED, name: 'shared-services', cidr: '10.30.0.0/16', subnets: 2, clusters: ['jenkins'], tasks: 2 },
    ],
  },
  [`${STAGING}/eu-central-1`]: {
    polled_at: NETWORKS.polled_at,
    items: [{ vpc_id: STAGING_VPC, name: 'staging-vpc', cidr: '10.40.0.0/16', subnets: 2, clusters: ['staging-web'], tasks: 2 }],
  },
}

function tree(vpcId: string, overrides: Partial<VpcTreeResponse> = {}): VpcTreeResponse {
  const name = vpcId === MAIN ? 'homelab-main' : vpcId === SHARED ? 'shared-services' : 'staging-vpc'
  return {
    polled_at: NETWORKS.polled_at,
    vpc: { vpc_id: vpcId, name, cidr: '10.20.0.0/16', account_id: HOMELAB, account_name: 'homelab-admin', region: 'us-east-1' },
    edges: [
      { kind: 'igw', id: 'igw-1' },
      { kind: 'nat', id: 'nat-1', subnet_id: 'subnet-aaa' },
      { kind: 'nat', id: 'nat-2', subnet_id: 'subnet-ccc' },
      { kind: 'endpoint', id: 'vpce-1', detail: 'logs' },
      { kind: 'endpoint', id: 'vpce-2', detail: 'ecr.dkr' },
    ],
    summary: { subnets: 3, zones: 2, running_tasks: 10, unplaced_tasks: 0, collapsed_by_default: false },
    root: buildTree(),
    ...overrides,
  }
}

function serveEstate(overrides: { tree?: (vpcId: string) => VpcTreeResponse } = {}) {
  server.use(
    http.get('/api/networks', () => HttpResponse.json(NETWORKS)),
    http.get('/api/networks/:account/:region', ({ params }) => {
      const body = REGION_VPCS[`${params.account}/${params.region}`]
      return body ? HttpResponse.json(body) : HttpResponse.json({ items: [], polled_at: NETWORKS.polled_at })
    }),
    http.get('/api/vpcs/:vpcId/tree', ({ params }) => HttpResponse.json((overrides.tree ?? tree)(String(params.vpcId)))),
  )
}

beforeEach(() => {
  localStorage.clear()
})

afterEach(async () => {
  await new Promise((resolve) => setTimeout(resolve, 100))
})

const vpcList = () => screen.getByRole('region', { name: 'VPCs' })
const vpcButton = (name: RegExp | string) => within(vpcList()).getByRole('button', { name })
const placement = () => screen.getByRole('region', { name: 'Placement' })

describe('Networks VPC list', () => {
  it('groups VPCs by account with region, CIDR, clusters and tasks', async () => {
    serveEstate()
    renderApp('/networks')
    await screen.findByRole('region', { name: 'VPCs' })
    expect(await within(vpcList()).findByRole('button', { name: /homelab-main/ })).toHaveTextContent('us-east-1 · 10.20.0.0/16')
    expect(vpcButton(/homelab-main/)).toHaveTextContent('2 cl · 10 t')
    expect(within(vpcList()).getByText('homelab-admin')).toBeInTheDocument()
    expect(within(vpcList()).getByText('staging-example')).toBeInTheDocument()
    expect(vpcButton(/shared-services/)).toBeInTheDocument()
    expect(vpcButton(/staging-vpc/)).toBeInTheDocument()
    expect(screen.getByText(/3 VPCs · 2 accounts/)).toBeInTheDocument()
  })

  it('says how many VPCs without tasks are hidden', async () => {
    serveEstate()
    renderApp('/networks')
    expect(await screen.findByText('1 VPC without tasks hidden')).toBeInTheDocument()
  })

  it('selects the first VPC by default and shows its details', async () => {
    serveEstate()
    renderApp('/networks')
    expect(await screen.findByRole('heading', { name: 'homelab-main', level: 2 })).toBeInTheDocument()
    expect(vpcButton(/homelab-main/)).toHaveAttribute('aria-pressed', 'true')
    expect(vpcButton(/shared-services/)).toHaveAttribute('aria-pressed', 'false')
  })

  it('clicking a VPC selects it and records it in the URL', async () => {
    const user = userEvent.setup()
    serveEstate()
    renderApp('/networks')
    await screen.findByRole('region', { name: 'VPCs' })
    await user.click(await within(vpcList()).findByRole('button', { name: /shared-services/ }))
    expect(await screen.findByRole('heading', { name: 'shared-services', level: 2 })).toBeInTheDocument()
    expect(vpcButton(/shared-services/)).toHaveAttribute('aria-pressed', 'true')
    expect(screen.getByTestId('search').textContent).toContain(`vpc=${encodeURIComponent(SHARED_KEY)}`)
  })

  it('an unknown ?vpc= key shows a no-access state instead of opening another VPC', async () => {
    serveEstate()
    renderApp(`/networks?vpc=${encodeURIComponent(`${HOMELAB}/us-east-1/vpc-gone`)}`)
    expect(await screen.findByText('No running tasks you can see in this VPC.')).toBeInTheDocument()
    expect(screen.queryByRole('heading', { name: 'homelab-main', level: 2 })).not.toBeInTheDocument()
    expect(await within(vpcList()).findByRole('button', { name: /homelab-main/ })).toHaveAttribute('aria-pressed', 'false')
  })

  it('restores the selected VPC and the filter from the URL', async () => {
    serveEstate()
    renderApp(`/networks?vpc=${encodeURIComponent(SHARED_KEY)}&q=us-east`)
    expect(await screen.findByRole('heading', { name: 'shared-services', level: 2 })).toBeInTheDocument()
    expect(screen.getByRole('searchbox', { name: 'Filter VPCs' })).toHaveValue('us-east')
    expect(within(vpcList()).queryByRole('button', { name: /staging-vpc/ })).not.toBeInTheDocument()
  })

  it('the filter matches VPC, account or region and is kept in the URL', async () => {
    const user = userEvent.setup()
    serveEstate()
    renderApp('/networks')
    await screen.findByRole('region', { name: 'VPCs' })
    await within(vpcList()).findByRole('button', { name: /homelab-main/ })
    await user.type(screen.getByRole('searchbox', { name: 'Filter VPCs' }), 'staging-example')
    await waitFor(() => expect(within(vpcList()).queryByRole('button', { name: /homelab-main/ })).not.toBeInTheDocument())
    expect(vpcButton(/staging-vpc/)).toBeInTheDocument()
    expect(screen.getByTestId('search').textContent).toContain('q=staging-example')

    await user.clear(screen.getByRole('searchbox', { name: 'Filter VPCs' }))
    await user.type(screen.getByRole('searchbox', { name: 'Filter VPCs' }), 'nothing-matches')
    expect(await screen.findByText('No VPCs match this filter.')).toBeInTheDocument()
  })
})

describe('Networks VPC detail', () => {
  it('shows a KPI strip for the selected VPC', async () => {
    serveEstate()
    renderApp('/networks')
    const strip = await screen.findByRole('list', { name: 'VPC summary' })
    const text = (label: string) => within(strip).getAllByRole('listitem').find((cell) => cell.textContent?.toUpperCase().startsWith(label))
    expect(text('SUBNETS')).toHaveTextContent('32 private · 1 public')
    expect(text('CLUSTERS')).toHaveTextContent('2')
    expect(text('RUNNING TASKS')).toHaveTextContent('10')
    expect(text('IPS USED')).toHaveTextContent('33of 4593')
    expect(text('NAT GATEWAYS')).toHaveTextContent('2one per zone')
  })

  it('lists subnets with their IP use', async () => {
    serveEstate()
    renderApp('/networks')
    const table = await screen.findByRole('table', { name: 'Subnets' })
    expect(within(table).getAllByRole('row')).toHaveLength(4)
    const privateA = within(within(table).getByRole('row', { name: /private-a/ }))
    expect(privateA.getByText('subnet-aaa')).toBeInTheDocument()
    expect(privateA.getByText('Private')).toBeInTheDocument()
    expect(privateA.getByText('eu-west-1a')).toBeInTheDocument()
    expect(privateA.getByText('10.20.1.0/24')).toBeInTheDocument()
    expect(privateA.getByText('27 of 251')).toBeInTheDocument()
    expect(within(within(table).getByRole('row', { name: /public-a/ })).getByText('Public')).toBeInTheDocument()
  })

  it('placement starts collapsed to clusters', async () => {
    serveEstate()
    renderApp('/networks')
    await screen.findByRole('region', { name: 'Placement' })
    const outline = within(placement())
    expect(outline.getByText('eu-west-1a')).toBeInTheDocument()
    expect(outline.getByText('private-a')).toBeInTheDocument()
    const core = outline.getByRole('button', { name: /core-services/ })
    expect(core).toHaveAttribute('aria-expanded', 'false')
    expect(outline.getByRole('button', { name: /ci-tools/ })).toBeInTheDocument()
    expect(outline.queryByRole('link', { name: 'payments-api' })).not.toBeInTheDocument()
    expect(outline.getByRole('button', { name: 'Expand all' })).toBeInTheDocument()
  })

  it('labels placement kinds in sentence case, not uppercase', async () => {
    serveEstate()
    renderApp('/networks')
    await screen.findByRole('region', { name: 'Placement' })
    const outline = within(placement())
    expect(outline.getAllByText('Zone').length).toBeGreaterThan(0)
    expect(outline.getAllByText('Subnet').length).toBeGreaterThan(0)
    expect(outline.getAllByText('Cluster').length).toBeGreaterThan(0)
    expect(outline.queryByText('ZONE')).not.toBeInTheDocument()
  })

  it('clicking a cluster opens its services, linked to the service page', async () => {
    const user = userEvent.setup()
    serveEstate()
    renderApp('/networks')
    await screen.findByRole('region', { name: 'Placement' })
    await user.click(within(placement()).getByRole('button', { name: /core-services/ }))
    expect(within(placement()).getByRole('button', { name: /core-services/ })).toHaveAttribute('aria-expanded', 'true')
    expect(within(placement()).getByRole('link', { name: 'payments-api' })).toHaveAttribute('href', '/services/svc-pay/overview')
    expect(within(placement()).getByRole('link', { name: 'orders-api' })).toBeInTheDocument()
    expect(within(placement()).queryByText('Standalone tasks')).not.toBeInTheDocument()

    await user.click(within(placement()).getByRole('button', { name: /core-services/ }))
    expect(within(placement()).queryByRole('link', { name: 'payments-api' })).not.toBeInTheDocument()
  })

  it('Expand all opens every cluster and Collapse to clusters closes them again', async () => {
    const user = userEvent.setup()
    serveEstate()
    renderApp('/networks')
    await screen.findByRole('region', { name: 'Placement' })
    await user.click(within(placement()).getByRole('button', { name: 'Expand all' }))
    expect(within(placement()).getByRole('link', { name: 'payments-api' })).toBeInTheDocument()
    expect(within(placement()).getByText('Standalone tasks')).toBeInTheDocument()

    await user.click(within(placement()).getByRole('button', { name: 'Collapse to clusters' }))
    expect(within(placement()).queryByRole('link', { name: 'payments-api' })).not.toBeInTheDocument()
    expect(within(placement()).getByRole('button', { name: /core-services/ })).toHaveAttribute('aria-expanded', 'false')
  })

  it('shows the internet gateway, NAT gateways and endpoints', async () => {
    serveEstate()
    renderApp('/networks')
    const panel = (await screen.findByRole('region', { name: 'Gateways and endpoints' })) as HTMLElement
    expect(within(panel).getByText('igw-1')).toBeInTheDocument()
    expect(within(panel).getByText('nat-1, nat-2')).toBeInTheDocument()
    expect(within(panel).getByText('ecr.dkr · logs')).toBeInTheDocument()
  })

  it('notes tasks that have no subnet', async () => {
    serveEstate({ tree: (id) => tree(id, { summary: { subnets: 3, zones: 2, running_tasks: 10, unplaced_tasks: 2, collapsed_by_default: false } }) })
    renderApp('/networks')
    expect(await screen.findByText('2 tasks without a subnet not shown')).toBeInTheDocument()
  })

  it('says so when the user can see no tasks in the VPC', async () => {
    server.use(
      http.get('/api/networks', () => HttpResponse.json(NETWORKS)),
      http.get('/api/networks/:account/:region', ({ params }) => HttpResponse.json(REGION_VPCS[`${params.account}/${params.region}`] ?? { items: [], polled_at: '' })),
      http.get('/api/vpcs/:vpcId/tree', () => HttpResponse.json({ error: { code: 'not_found', message: 'Not found.' } }, { status: 404 })),
    )
    renderApp('/networks')
    expect(await screen.findByText('No running tasks you can see in this VPC.')).toBeInTheDocument()
  })
})

describe('Networks deep links', () => {
  it('a VPC URL from before the redesign selects that VPC', async () => {
    serveEstate()
    renderApp(`/networks/${HOMELAB}/us-east-1/${SHARED}`)
    expect(await screen.findByRole('heading', { name: 'shared-services', level: 2 })).toBeInTheDocument()
    expect(screen.getByTestId('location').textContent).toBe('/networks')
    expect(screen.getByTestId('search').textContent).toContain(`vpc=${encodeURIComponent(SHARED_KEY)}`)
  })

  it('a region URL from before the redesign filters the list to that region', async () => {
    serveEstate()
    renderApp(`/networks/${STAGING}/eu-central-1`)
    await screen.findByRole('region', { name: 'VPCs' })
    expect(await within(vpcList()).findByRole('button', { name: /staging-vpc/ })).toBeInTheDocument()
    expect(screen.getByTestId('location').textContent).toBe('/networks')
    expect(screen.getByRole('searchbox', { name: 'Filter VPCs' })).toHaveValue('eu-central-1')
    expect(within(vpcList()).queryByRole('button', { name: /homelab-main/ })).not.toBeInTheDocument()
  })

  it('shows one stale-data notice naming Networks when its poll is failing', async () => {
    serveEstate()
    server.use(
      http.get('/api/poller', () =>
        HttpResponse.json({
          items: [
            {
              account_id: HOMELAB,
              account_name: 'homelab-admin',
              region: 'us-east-1',
              throttled: false,
              failing: false,
              last_error: '',
              last_ok_at: '2026-11-01T10:00:00Z',
              tiers: [
                {
                  tier: 'network',
                  label: 'Networks',
                  core: false,
                  failing: true,
                  throttled: false,
                  last_ok_at: null,
                  last_error: 'DescribeVpcs: AccessDenied',
                  interval_ms: 300000,
                },
              ],
            },
          ],
        }),
      ),
    )
    renderApp('/networks')
    expect(await screen.findByText('Some data on this page is stale: Networks')).toBeInTheDocument()
  })
})
