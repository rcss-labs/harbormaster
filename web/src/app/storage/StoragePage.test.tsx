import { http, HttpResponse } from 'msw'
import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'
import type { StorageItem, StorageResponse } from '../../api/storage'
import { renderWithProviders } from '../../test/setup'
import { server } from '../../test/msw'
import { AuthProvider } from '../auth/AuthProvider'
import { StoragePage } from './StoragePage'

/** Thirty daily points rising linearly from `from` to `to`, like the mockup's size and throughput histories. */
function trend(from: number, to: number) {
  return Array.from({ length: 30 }, (_, day) => ({
    t: `2026-10-${String(day + 2).padStart(2, '0')}T00:00:00Z`,
    v: from + ((to - from) * day) / 29,
  }))
}

const JENKINS: StorageItem = {
  file_system_id: 'fs-0c91a4e7',
  name: 'jenkins-home',
  account_id: '482109375512',
  region: 'us-east-1',
  vpc: { vpc_id: 'vpc-0a1', name: 'shared-services' },
  size_bytes: 41231686041,
  throughput_mode: 'Elastic',
  performance_mode: 'generalPurpose',
  mounted: true,
  client_connections: 1,
  mounting_tasks: 1,
  access_points: [{ access_point_id: 'fsap-0a1b2c3d4e', path: '/jenkins', uid: 1000 }],
  mounted_by: [
    {
      service_id: 'svc-jenkins-controller',
      service: 'jenkins-controller',
      cluster: 'ci-tools',
      container_path: '/var/jenkins_home',
      tasks: 1,
    },
  ],
  size_30d: trend(38, 41.2),
  growth_30d_pct: 29,
  throughput_24h: trend(20, 29),
}

const UPLOADS: StorageItem = {
  file_system_id: 'fs-07a2b9e1',
  name: 'uploads-archive',
  account_id: '482109375512',
  region: 'us-east-1',
  vpc: { vpc_id: 'vpc-0b2', name: 'homelab-main' },
  size_bytes: 227204155801,
  throughput_mode: 'Elastic',
  performance_mode: 'generalPurpose',
  mounted: false,
  client_connections: null,
  mounting_tasks: 0,
  access_points: [],
  mounted_by: [],
  size_30d: trend(200, 211.6),
  growth_30d_pct: 28,
  throughput_24h: trend(15, 20),
}

const OTHER_FILE_SYSTEMS: StorageItem[] = [
  { ...JENKINS, file_system_id: 'fs-02e7d1c9', name: 'reports-data', size_bytes: 12992276070, access_points: [], mounted_by: [] },
  { ...JENKINS, file_system_id: 'fs-0a6b33f0', name: 'shared-certs', region: 'eu-central-1', size_bytes: 21474836, client_connections: 3 },
  { ...JENKINS, file_system_id: 'fs-0d18c2ab', name: 'search-index', region: 'eu-west-1', size_bytes: 80423262618 },
]

const STORAGE: StorageResponse = {
  polled_at: '2026-11-01T10:00:00Z',
  items: [JENKINS, ...OTHER_FILE_SYSTEMS, UPLOADS],
  totals: { size_bytes: 361872855366, mounted: 4, not_mounted: 1 },
}

function renderStoragePage(route = '/storage') {
  return renderWithProviders(
    <AuthProvider>
      <StoragePage />
    </AuthProvider>,
    { route },
  )
}

function serveStorage(response: StorageResponse) {
  server.use(http.get('/api/storage', () => HttpResponse.json(response)))
}

/** Names of the file systems in the table body, top to bottom. */
const tableNames = () =>
  within(screen.getByRole('table', { name: 'File systems' }))
    .getAllByRole('row')
    .slice(1)
    .map((row) => within(row).getAllByRole('cell')[0].querySelector('strong')?.textContent)

describe('StoragePage', () => {
  it('shows the KPI strip with the size of the not-mounted, still-billed file systems', async () => {
    serveStorage(STORAGE)
    renderStoragePage()
    const strip = within(await screen.findByRole('list', { name: 'Storage summary' }))
    expect(strip.getByText('File systems').parentElement).toHaveTextContent('5')
    expect(strip.getByText('Total stored').parentElement).toHaveTextContent('337.0 GiB')
    expect(strip.getByText('Not mounted, still billed').parentElement).toHaveTextContent('211.6 GiB')
    expect(strip.getByText('Not mounted, still billed').parentElement).toHaveTextContent('1 file system')
  })

  it('lists file systems largest first', async () => {
    serveStorage(STORAGE)
    renderStoragePage()
    await screen.findByRole('table', { name: 'File systems' })
    expect(tableNames()).toEqual(['uploads-archive', 'search-index', 'jenkins-home', 'reports-data', 'shared-certs'])
  })

  it('flags a not-mounted file system with the warn stripe and the label, and mounted ones without', async () => {
    serveStorage(STORAGE)
    renderStoragePage()
    const rows = within(await screen.findByRole('table', { name: 'File systems' })).getAllByRole('row').slice(1)
    expect(rows[0]).toHaveAttribute('data-status', 'warn')
    expect(within(rows[0]).getByText('Not mounted')).toBeInTheDocument()
    expect(rows[1]).not.toHaveAttribute('data-status')
  })

  it('shows the filter with counts and filters to Not mounted', async () => {
    serveStorage(STORAGE)
    renderStoragePage()
    const filter = await screen.findByRole('radiogroup', { name: 'Mounted state' })
    expect(within(filter).getByRole('radio', { name: 'All 5' })).toHaveAttribute('aria-checked', 'true')
    expect(within(filter).getByRole('radio', { name: 'Mounted 4' })).toBeInTheDocument()
    await userEvent.click(within(filter).getByRole('radio', { name: 'Not mounted 1' }))
    expect(tableNames()).toEqual(['uploads-archive'])
    await userEvent.click(within(filter).getByRole('radio', { name: 'Mounted 4' }))
    expect(tableNames()).toEqual(['search-index', 'jenkins-home', 'reports-data', 'shared-certs'])
  })

  it('restores the segment and query from the URL', async () => {
    serveStorage(STORAGE)
    renderStoragePage('/storage?seg=mounted&q=fs-0c91')
    await screen.findByRole('table', { name: 'File systems' })
    expect(tableNames()).toEqual(['jenkins-home'])
    expect(screen.getByRole('searchbox', { name: 'Search file systems' })).toHaveValue('fs-0c91')
  })

  it('filters by name, id, region or service with the query', async () => {
    serveStorage(STORAGE)
    renderStoragePage()
    await screen.findByRole('table', { name: 'File systems' })
    await userEvent.type(screen.getByRole('searchbox', { name: 'Search file systems' }), 'eu-central-1')
    expect(tableNames()).toEqual(['shared-certs'])
  })

  it('shows client count, growth, throughput, access points and mounting service links', async () => {
    serveStorage(STORAGE)
    renderStoragePage('/storage?q=jenkins-home')
    const row = within((await screen.findAllByRole('row'))[1])
    expect(row.getByText('fs-0c91a4e7 · us-east-1 · shared-services')).toBeInTheDocument()
    expect(row.getByText('+29% in 30 days')).toBeInTheDocument()
    expect(row.getByText('29 MiB/s')).toBeInTheDocument()
    expect(row.getByText('1 access point')).toBeInTheDocument()
    expect(row.getByText('1 client')).toBeInTheDocument()
    expect(row.getByRole('link', { name: 'jenkins-controller' })).toHaveAttribute('href', '/services/svc-jenkins-controller/overview')
    expect(row.getByText('(ci-tools)')).toBeInTheDocument()
  })

  it('shows a dash when the metrics are null or empty', async () => {
    serveStorage({
      ...STORAGE,
      items: [{ ...JENKINS, size_30d: null, throughput_24h: [], growth_30d_pct: null }],
    })
    renderStoragePage()
    const row = within((await screen.findAllByRole('row'))[1])
    expect(row.queryByText(/in 30 days/)).not.toBeInTheDocument()
    expect(row.queryByText(/MiB\/s/)).not.toBeInTheDocument()
    expect(row.getAllByText('—').length).toBeGreaterThanOrEqual(2)
  })

  it('says no service mounts an unmounted file system', async () => {
    serveStorage(STORAGE)
    renderStoragePage('/storage?seg=unmounted')
    expect(await screen.findByText('No service mounts this file system.')).toBeInTheDocument()
  })

  it('shows the empty state when there are no file systems', async () => {
    serveStorage({ polled_at: '2026-11-01T10:00:00Z', items: [], totals: { size_bytes: 0, mounted: 0, not_mounted: 0 } })
    renderStoragePage()
    expect(await screen.findByText('No file systems you can see.')).toBeInTheDocument()
  })

  it('shows one stale-data notice naming Storage when its poll is failing', async () => {
    serveStorage(STORAGE)
    server.use(
      http.get('/api/poller', () =>
        HttpResponse.json({
          items: [
            {
              account_id: JENKINS.account_id,
              account_name: 'homelab-admin',
              region: 'us-east-1',
              throttled: false,
              failing: false,
              last_error: '',
              last_ok_at: '2026-11-01T10:00:00Z',
              tiers: [
                {
                  tier: 'storage',
                  label: 'Storage',
                  core: false,
                  failing: true,
                  throttled: false,
                  last_ok_at: null,
                  last_error: 'DescribeFileSystems: AccessDenied',
                  interval_ms: 300000,
                },
              ],
            },
          ],
        }),
      ),
    )
    renderStoragePage()
    expect(await screen.findByText('Some data on this page is stale: Storage')).toBeInTheDocument()
  })
})
