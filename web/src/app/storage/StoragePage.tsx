import { useMemo, useState } from 'react'
import { Link, useSearchParams } from 'react-router'
import { type StorageItem, useStorage } from '../../api/storage'
import { DataBanner } from '../../components/DataBanner'
import { DataTable, type DataTableColumn, type SortState } from '../../components/DataTable'
import { Drawer } from '../../components/Drawer'
import { EmptyState } from '../../components/EmptyState'
import { Icon } from '../../components/Icon'
import { KpiStrip } from '../../components/KpiStrip'
import { Panel } from '../../components/Panel'
import { SegmentedFilter } from '../../components/Seg'
import { SortFieldsButton } from '../../components/SortFieldsButton'
import { StatusDot } from '../../components/StatusDot'
import { usePhone } from '../../components/useNarrow'
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

const DEFAULT_SORT: SortState = { key: 'size', dir: 'desc' }

const SORT_OPTIONS = [
  { key: 'name', label: 'File system' },
  { key: 'size', label: 'Size' },
  { key: 'growth', label: 'Growth' },
  { key: 'throughput', label: 'Throughput' },
]

/** The phone list shows the mount state as a status pill on the name line; the mounting services live in the details sheet. */
const mountStatus = (fs: StorageItem) =>
  fs.mounted_by.length === 0 ? <StatusDot kind="warn">Not mounted</StatusDot> : <StatusDot kind="ok">{plural(clientsOf(fs), 'client')}</StatusDot>

const storageColumns = (phone: boolean): DataTableColumn<StorageItem>[] => [
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
    phone: phone ? 'status' : undefined,
    render: (fs) =>
      phone ? mountStatus(fs) : fs.mounted_by.length === 0 ? (
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
  const phone = usePhone()
  const columns = useMemo(() => storageColumns(phone), [phone])
  const [sort, setSort] = useState<SortState | null>(DEFAULT_SORT)
  const [detailId, setDetailId] = useState<string | null>(null)
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

  const detail = items.find((fs) => fs.file_system_id === detailId) ?? null

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
              {phone && <SortFieldsButton sortOptions={SORT_OPTIONS} sort={sort} onSortChange={setSort} defaults={{ sort: DEFAULT_SORT }} />}
            </>
          }
        >
          <DataTable
            label="File systems"
            columns={columns}
            rows={visible}
            rowKey={(fs) => fs.file_system_id}
            minWidth={960}
            sort={sort}
            onSortChange={setSort}
            rowStatus={(fs) => (fs.mounted ? undefined : 'warn')}
            empty="No file systems match."
            rowAction={
              phone
                ? (fs) => (
                    <button type="button" className={styles.details} aria-label={`Details for ${fs.name}`} aria-haspopup="dialog" onClick={() => setDetailId(fs.file_system_id)}>
                      <Icon name="more" />
                    </button>
                  )
                : undefined
            }
          />
        </Panel>
      </div>
      {detail && (
        <Drawer open onClose={() => setDetailId(null)} title={detail.name} sub={[detail.file_system_id, detail.region, detail.vpc?.name].filter(Boolean).join(' · ')}>
          <StorageDetail fs={detail} />
        </Drawer>
      )}
    </>
  )
}

/** The phone details sheet: the figures of the row, the access points and the services that mount the file system. */
function StorageDetail({ fs }: { fs: StorageItem }) {
  const latest = latestThroughput(fs)
  return (
    <>
      <dl className={styles.kv}>
        <dt>Size</dt>
        <dd>{formatSize(fs.size_bytes)}</dd>
        <dt>Growth</dt>
        <dd>{fs.growth_30d_pct === null ? '—' : growthLabel(fs.growth_30d_pct)}</dd>
        <dt>Throughput</dt>
        <dd>
          {latest === undefined ? '—' : formatMiBps(latest)} <span className={styles.sub}>{fs.throughput_mode}</span>
        </dd>
        <dt>Access points</dt>
        <dd>{fs.access_points.length === 0 ? 'None' : plural(fs.access_points.length, 'access point')}</dd>
      </dl>
      {fs.access_points.length > 0 && (
        <ul className={styles.list}>
          {fs.access_points.map((ap) => (
            <li key={ap.access_point_id} className={styles.mono}>
              {`${ap.access_point_id} · ${ap.path} · uid ${ap.uid ?? '—'}`}
            </li>
          ))}
        </ul>
      )}
      <h3 className={styles.heading}>Mounted by</h3>
      {fs.mounted_by.length === 0 ? (
        <div className={styles.warn}>No service mounts this file system.</div>
      ) : (
        <ul className={styles.list}>
          {fs.mounted_by.map((mount, index) => (
            <li key={index} className={styles.mount}>
              <Link to={`/services/${mount.service_id}/overview`} className={styles.link}>
                {mount.service}
              </Link>{' '}
              <span className={styles.sub}>{`(${mount.cluster})`}</span>
              <span className={styles.mono}> {mount.container_path}</span>
            </li>
          ))}
        </ul>
      )}
    </>
  )
}

function PageHead() {
  return (
    <div className={styles.pageHead}>
      <div className={styles.titles}>
        <h1 className="hm-phone-title-sr">Storage</h1>
        <div className={styles.meta}>EFS file systems and the services that mount them. File systems are created by Terraform.</div>
      </div>
    </div>
  )
}
