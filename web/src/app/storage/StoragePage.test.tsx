import { http, HttpResponse } from 'msw'
import { screen, within } from '@testing-library/react'
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

function renderStoragePage() {
  return renderWithProviders(
    <AuthProvider>
      <StoragePage />
    </AuthProvider>,
    { route: '/storage' },
  )
}

function serveStorage(response: StorageResponse) {
  server.use(http.get('/api/storage', () => HttpResponse.json(response)))
}

/** The card whose heading is `title`; the card's head is the heading's parent and the card its grandparent. */
const findCardTitled = async (title: string) =>
  (await screen.findByRole('heading', { name: title })).parentElement!.parentElement!

describe('StoragePage', () => {
  it('lists each file system in the stored-data chart with its size and the total', async () => {
    serveStorage(STORAGE)
    renderStoragePage()
    const bars = within(await findCardTitled('Stored data by file system'))
    expect(bars.getByText('jenkins-home')).toBeInTheDocument()
    for (const name of ['reports-data', 'shared-certs', 'search-index', 'uploads-archive']) {
      expect(bars.getByText(name)).toBeInTheDocument()
    }
    expect(bars.getByText('337.0 GiB in total')).toBeInTheDocument()
  })

  it('counts mounted and unmounted file systems in the donut legend', async () => {
    serveStorage(STORAGE)
    renderStoragePage()
    const donut = within(await findCardTitled('Mounted or not'))
    expect(donut.getByText('Mounted by a service')).toBeInTheDocument()
    expect(donut.getByText('Mounted by a service').parentElement).toHaveTextContent(/^Mounted by a service4$/)
    expect(donut.getByText('Not mounted, still billed').parentElement).toHaveTextContent(/^Not mounted, still billed1$/)
  })

  it('shows the mounted file system card with its client count, growth, access point and mount', async () => {
    serveStorage(STORAGE)
    renderStoragePage()
    const card = within(await screen.findByRole('region', { name: 'jenkins-home' }))
    expect(card.getByText('fs-0c91a4e7 · us-east-1 · shared-services')).toBeInTheDocument()
    expect(card.getByText('1 client')).toBeInTheDocument()
    expect(card.getByText('+29% in 30 days')).toBeInTheDocument()
    expect(card.getByText('Elastic')).toBeInTheDocument()
    expect(card.getByText('fsap-0a1b2c3d4e · /jenkins · uid 1000')).toBeInTheDocument()
    expect(card.getByRole('link', { name: 'jenkins-controller' })).toHaveAttribute(
      'href',
      '/services/svc-jenkins-controller/overview',
    )
    expect(card.getByText('(ci-tools) →')).toBeInTheDocument()
    expect(card.getByText('/var/jenkins_home')).toBeInTheDocument()
    expect(card.getByRole('link', { name: 'jenkins-controller' }).parentElement).toHaveTextContent(
      'jenkins-controller (ci-tools) → /var/jenkins_home',
    )
  })

  it('colours the size sparkline with the same series colour as the file system bar', async () => {
    serveStorage(STORAGE)
    renderStoragePage()
    const region = await screen.findByRole('region', { name: 'jenkins-home' })
    expect(region.querySelector('path[stroke]')).toHaveAttribute('stroke', 'var(--series-1)')
  })

  it('marks an unmounted file system with no access points and no mounts', async () => {
    serveStorage(STORAGE)
    renderStoragePage()
    const card = within(await screen.findByRole('region', { name: 'uploads-archive' }))
    expect(card.getByText('Not mounted')).toBeInTheDocument()
    expect(card.getByText('None')).toBeInTheDocument()
    expect(card.getByText('No service mounts this file system.')).toBeInTheDocument()
  })

  it('shows Metrics unavailable in place of the sparklines when both series are null', async () => {
    serveStorage({
      ...STORAGE,
      items: [{ ...JENKINS, size_30d: null, throughput_24h: null, growth_30d_pct: null }],
    })
    renderStoragePage()
    expect(await screen.findByText('Metrics unavailable')).toBeInTheDocument()
    expect(screen.queryByText('+29% in 30 days')).not.toBeInTheDocument()
    expect((await screen.findByRole('region', { name: 'jenkins-home' })).querySelector('svg')).toBeNull()
  })

  it('treats empty series like null: Metrics unavailable and no sparklines', async () => {
    serveStorage({
      ...STORAGE,
      items: [{ ...JENKINS, size_30d: [], throughput_24h: [], growth_30d_pct: null }],
    })
    renderStoragePage()
    expect(await screen.findByText('Metrics unavailable')).toBeInTheDocument()
    expect((await screen.findByRole('region', { name: 'jenkins-home' })).querySelector('svg')).toBeNull()
  })

  it('shows the empty state when there are no file systems', async () => {
    serveStorage({ polled_at: '2026-11-01T10:00:00Z', items: [], totals: { size_bytes: 0, mounted: 0, not_mounted: 0 } })
    renderStoragePage()
    expect(await screen.findByText('No file systems you can see.')).toBeInTheDocument()
  })
})
