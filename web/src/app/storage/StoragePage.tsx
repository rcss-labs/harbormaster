import { Link, useSearchParams } from 'react-router'
import { type StorageItem, useStorage } from '../../api/storage'
import { DataBanner } from '../../components/DataBanner'
import { DataTable, type DataTableColumn } from '../../components/DataTable'
import { EmptyState } from '../../components/EmptyState'
import { KpiStrip } from '../../components/KpiStrip'
import { Panel } from '../../components/Panel'
import { SegmentedFilter } from '../../components/Seg'
import { StatusDot } from '../../components/StatusDot'
import { useCrumbs } from '../shell/crumbs'
import { formatMiBps, formatSize } from './format'
import styles from './storage.module.css'

type Segment = 'all' | 'mounted' | 'unmounted'

const SEGMENTS: Segment[] = ['all', 'mounted', 'unmounted']
const isSegment = (value: string | null): value is Segment => SEGMENTS.includes(value as Segment)

const plural = (count: number, noun: string) => `${count} ${noun}${count === 1 ? '' : 's'}`

/** Growth rounded to a whole percent with its sign, e.g. "+29% in 30 days" or "-3% in 30 days". */
const growthLabel = (pct: number) => {
  const rounded = Math.round(pct)
  return `${rounded >= 0 ? '+' : ''}${rounded}% in 30 days`
}

const clientsOf = (fs: StorageItem) => fs.client_connections ?? fs.mounting_tasks

const searchText = (fs: StorageItem) =>
  [fs.name, fs.file_system_id, fs.region, fs.vpc?.name, ...fs.mounted_by.flatMap((mount) => [mount.service, mount.cluster])]
    .filter(Boolean)
    .join(' ')
    .toLowerCase()

// CloudWatch returning no datapoints is treated like a failed call: both mean there is nothing to show.
const latestThroughput = (fs: StorageItem) => fs.throughput_24h?.at(-1)?.v

const COLUMNS: DataTableColumn<StorageItem>[] = [
  {
    key: 'name',
    header: 'File system',
    width: 'minmax(200px, 2fr)',
    sortValue: (fs) => fs.name,
    render: (fs) => (
      <>
        <strong className={styles.name}>{fs.name}</strong>
        <div className={styles.sub}>{[fs.file_system_id, fs.region, fs.vpc?.name].filter(Boolean).join(' · ')}</div>
      </>
    ),
  },
  {
    key: 'size',
    header: 'Size',
    width: '110px',
    align: 'right',
    sortValue: (fs) => fs.size_bytes,
    render: (fs) => formatSize(fs.size_bytes),
  },
  {
    key: 'growth',
    header: 'Growth',
    width: '140px',
    sortValue: (fs) => fs.growth_30d_pct,
    render: (fs) => (fs.growth_30d_pct === null ? '—' : <span className={styles.sub}>{growthLabel(fs.growth_30d_pct)}</span>),
  },
  {
    key: 'throughput',
    header: 'Throughput',
    width: '130px',
    sortValue: (fs) => latestThroughput(fs),
    render: (fs) => {
      const latest = latestThroughput(fs)
      return (
        <>
          {latest === undefined ? '—' : formatMiBps(latest)}
          <div className={styles.sub}>{fs.throughput_mode}</div>
        </>
      )
    },
  },
  {
    key: 'access',
    header: 'Access points',
    width: '170px',
    render: (fs) =>
      fs.access_points.length === 0 ? (
        <span className={styles.sub}>None</span>
      ) : (
        <span title={fs.access_points.map((ap) => `${ap.access_point_id} · ${ap.path} · uid ${ap.uid ?? '—'}`).join('\n')}>
          {plural(fs.access_points.length, 'access point')}
        </span>
      ),
  },
  {
    key: 'mounted',
    header: 'Mounted by',
    width: 'minmax(220px, 2fr)',
    render: (fs) =>
      fs.mounted_by.length === 0 ? (
        <>
          <StatusDot kind="warn">Not mounted</StatusDot>
          <div className={styles.warn}>No service mounts this file system.</div>
        </>
      ) : (
        <>
          <StatusDot kind="ok">{plural(clientsOf(fs), 'client')}</StatusDot>
          {fs.mounted_by.map((mount, index) => (
            <div key={index} className={styles.mount}>
              <Link to={`/services/${mount.service_id}/overview`} className={styles.link}>
                {mount.service}
              </Link>{' '}
              <span className={styles.sub}>{`(${mount.cluster})`}</span>
              <span className={styles.mono}> {mount.container_path}</span>
            </div>
          ))}
        </>
      ),
  },
]

export function StoragePage() {
  useCrumbs([['Storage']])
  const storage = useStorage()
  const [params, setParams] = useSearchParams()
  const items = storage.data?.items

  const rawSegment = params.get('seg')
  const segment: Segment = isSegment(rawSegment) ? rawSegment : 'all'
  const query = params.get('q') ?? ''

  const update = (key: 'seg' | 'q', value: string, fallback: string) =>
    setParams(
      (current) => {
        const next = new URLSearchParams(current)
        if (value === fallback) next.delete(key)
        else next.set(key, value)
        return next
      },
      { replace: true },
    )

  if (storage.isError) {
    return (
      <>
        <PageHead />
        <EmptyState>{storage.error instanceof Error ? storage.error.message : 'The storage could not be loaded.'}</EmptyState>
      </>
    )
  }
  if (!items) return <PageHead />
  if (items.length === 0) {
    return (
      <>
        <PageHead />
        <EmptyState>No file systems you can see.</EmptyState>
      </>
    )
  }

  const mounted = items.filter((fs) => fs.mounted)
  const unmounted = items.filter((fs) => !fs.mounted)
  const totalBytes = items.reduce((sum, fs) => sum + fs.size_bytes, 0)
  const unmountedBytes = unmounted.reduce((sum, fs) => sum + fs.size_bytes, 0)

  const needle = query.trim().toLowerCase()
  const visible = items
    .filter((fs) => (segment === 'all' ? true : segment === 'mounted' ? fs.mounted : !fs.mounted))
    .filter((fs) => !needle || searchText(fs).includes(needle))

  return (
    <>
      <PageHead />
      <div className={styles.stack}>
        <DataBanner tiers={['Storage']} />
        <KpiStrip
          label="Storage summary"
          items={[
            { label: 'File systems', value: items.length },
            { label: 'Total stored', value: formatSize(totalBytes) },
            { label: 'Mounted', value: mounted.length },
            {
              label: 'Not mounted, still billed',
              value: (
                <>
                  {formatSize(unmountedBytes)} <span className={styles.kpiSub}>{plural(unmounted.length, 'file system')}</span>
                </>
              ),
              valueColor: unmounted.length > 0 ? 'var(--warning)' : undefined,
            },
          ]}
        />
        <Panel
          title="File systems"
          flush
          toolbar={
            <>
              <SegmentedFilter
                label="Mounted state"
                value={segment}
                onChange={(value) => update('seg', value, 'all')}
                options={[
                  { value: 'all', label: 'All', count: items.length },
                  { value: 'mounted', label: 'Mounted', count: mounted.length },
                  { value: 'unmounted', label: 'Not mounted', count: unmounted.length },
                ]}
              />
              <input
                type="search"
                className={styles.search}
                aria-label="Search file systems"
                placeholder="Search name, id, region or service"
                value={query}
                onChange={(event) => update('q', event.target.value, '')}
              />
            </>
          }
        >
          <DataTable
            label="File systems"
            columns={COLUMNS}
            rows={visible}
            rowKey={(fs) => fs.file_system_id}
            minWidth={960}
            defaultSort={{ key: 'size', dir: 'desc' }}
            rowStatus={(fs) => (fs.mounted ? undefined : 'warn')}
            empty="No file systems match."
          />
        </Panel>
      </div>
    </>
  )
}

function PageHead() {
  return (
    <div className={styles.pageHead}>
      <div className={styles.titles}>
        <h1>Storage</h1>
        <div className={styles.meta}>EFS file systems and the services that mount them. File systems are created by Terraform.</div>
      </div>
    </div>
  )
}
