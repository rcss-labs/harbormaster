import { useMemo } from 'react'
import { useSearchParams } from 'react-router'
import { useSecurity, type SecurityImage } from '../../api/security'
import { formatAgo, useNow } from '../../components/Ago'
import { Banner } from '../../components/Banner'
import { DataTable, type DataTableColumn } from '../../components/DataTable'
import { Drawer } from '../../components/Drawer'
import { EmptyState } from '../../components/EmptyState'
import { Panel } from '../../components/Panel'
import { Pill } from '../../components/Pill'
import { SegmentedFilter } from '../../components/Seg'
import { usePhone } from '../../components/usePhone'
import { useCrumbs } from '../shell/crumbs'
import { SECURITY_COPY } from './copy'
import { dbBadge, formatDateTime } from './freshness'
import { ImageDetail, StatusChip } from './ImageDetail'
import styles from './security.module.css'

type Segment = 'all' | 'critical' | 'not-scanned'
const SEGMENTS: Segment[] = ['all', 'critical', 'not-scanned']
const isSegment = (value: string | null): value is Segment => SEGMENTS.includes(value as Segment)

const isNotScanned = (image: SecurityImage) => image.status !== 'scanned'

const searchText = (image: SecurityImage) =>
  [image.image, ...image.requests.flatMap((request) => [request.service, request.cluster])].join(' ').toLowerCase()

const latestRequest = (image: SecurityImage) => [...image.requests].sort((a, b) => b.at.localeCompare(a.at))[0]

const SHORT_DIGEST_LENGTH = 19 // "sha256:" plus 12 hex characters

const count = (value: number) => <span className={value === 0 ? styles.zero : styles.count}>{value}</span>

const imageColumns = (): DataTableColumn<SecurityImage>[] => [
  {
    key: 'image',
    header: 'Image',
    width: 'minmax(260px, 3fr)',
    render: (image) => (
      <>
        <strong className={styles.name}>{imageName(image)}</strong>
        <div className={styles.subLine}>
          <span>{[...new Set(image.requests.map((request) => request.service))].join(', ')}</span>
          <StatusChip status={image.status} />
        </div>
      </>
    ),
  },
  {
    key: 'requested',
    header: 'Requested',
    width: '150px',
    render: (image) => <RequestedCell image={image} />,
  },
  { key: 'crit', header: 'Crit', width: '70px', align: 'right', phoneLabel: 'Crit', render: (image) => count(image.open.critical) },
  { key: 'high', header: 'High', width: '70px', align: 'right', phoneLabel: 'High', render: (image) => count(image.open.high) },
  { key: 'med', header: 'Med', width: '70px', align: 'right', phoneLabel: 'Med', render: (image) => count(image.open.medium) },
]

/** The image reference without its registry host, which every row of one estate shares. */
const imageName = (image: SecurityImage) => `${image.repository}:${image.tag}`

function RequestedCell({ image }: { image: SecurityImage }) {
  const now = useNow(15_000)
  const request = latestRequest(image)
  return (
    <>
      {formatAgo(image.last_requested_at, now)}
      {request && <div className={styles.sub}>{request.by}</div>}
    </>
  )
}

export function SecurityPage() {
  useCrumbs([[SECURITY_COPY.crumbRoot, '/'], [SECURITY_COPY.heading]])
  const security = useSecurity()
  const [params, setParams] = useSearchParams()
  const phone = usePhone()
  const now = useNow(15_000)
  const columns = useMemo(() => imageColumns(), [])

  const rawFilter = params.get('filter')
  const segment: Segment = isSegment(rawFilter) ? rawFilter : 'all'
  const query = params.get('q') ?? ''
  const selectedDigest = params.get('image') ?? ''

  const update = (key: 'filter' | 'q' | 'image', value: string, fallback: string) =>
    setParams(
      (current) => {
        const next = new URLSearchParams(current)
        if (value === fallback) next.delete(key)
        else next.set(key, value)
        return next
      },
      { replace: true },
    )

  const data = security.data
  const head = (
    <div className={styles.pageHead}>
      <div className={styles.titles}>
        <h1 className="hm-phone-title-sr">{SECURITY_COPY.heading}</h1>
        {data?.enabled && <DbBadgePill now={now} db={data.db} />}
      </div>
    </div>
  )

  if (security.isError) {
    return (
      <>
        {head}
        <EmptyState>{security.error instanceof Error ? security.error.message : SECURITY_COPY.listFailed}</EmptyState>
      </>
    )
  }
  if (!data) return head

  const banners = (
    <>
      <Banner kind="info">{data.note}</Banner>
      {!data.enabled && <Banner kind="info">{data.hint}</Banner>}
      {data.enabled && data.scanner.failing_since && (
        <Banner kind="bad">{SECURITY_COPY.scannerFailing(formatDateTime(data.scanner.failing_since), data.scanner.last_error)}</Banner>
      )}
      {data.enabled && data.db.freshness === 'stale' && (
        <Banner kind="warn">{[SECURITY_COPY.dbStale, data.db.last_refresh_error].filter(Boolean).join(' ')}</Banner>
      )}
    </>
  )
  if (!data.enabled) {
    return (
      <>
        {head}
        <div className={styles.stack}>{banners}</div>
      </>
    )
  }

  const needle = query.trim().toLowerCase()
  const visible = data.images
    .filter((image) => (segment === 'critical' ? image.open.critical > 0 : segment === 'not-scanned' ? isNotScanned(image) : true))
    .filter((image) => !needle || searchText(image).includes(needle))
  const selected = data.images.find((image) => image.digest === selectedDigest) ?? null
  const closeDetail = () => update('image', '', '')

  return (
    <>
      {head}
      <div className={styles.stack}>
        {banners}
        <Panel
          title="Images"
          flush
          toolbar={
            <>
              <SegmentedFilter
                label="Scan state"
                value={segment}
                onChange={(value) => update('filter', value, 'all')}
                options={[
                  { value: 'all', label: 'All' },
                  { value: 'critical', label: 'Critical' },
                  { value: 'not-scanned', label: 'Not scanned' },
                ]}
              />
              <input
                type="search"
                className={styles.search}
                aria-label="Search images"
                placeholder={SECURITY_COPY.searchPlaceholder}
                value={query}
                onChange={(event) => update('q', event.target.value, '')}
              />
            </>
          }
        >
          <DataTable
            label="Images"
            columns={columns}
            rows={visible}
            rowKey={(image) => image.digest}
            minWidth={680}
            onRowClick={(image) => update('image', image.digest, '')}
            rowStatus={(image) => (image.open.critical > 0 ? 'bad' : image.status === 'failed' ? 'warn' : undefined)}
            empty={data.images.length === 0 ? SECURITY_COPY.noImages : SECURITY_COPY.noMatches}
          />
        </Panel>
        {selected && !phone && (
          <Panel title={imageName(selected)} sub={selected.digest.slice(0, SHORT_DIGEST_LENGTH)} label="Image detail" flush>
            <ImageDetail digest={selected.digest} />
          </Panel>
        )}
      </div>
      {selected && phone && (
        <Drawer open onClose={closeDetail} title={imageName(selected)} sub={selected.digest.slice(0, SHORT_DIGEST_LENGTH)}>
          <ImageDetail digest={selected.digest} />
        </Drawer>
      )}
    </>
  )
}

function DbBadgePill({ db, now }: { db: NonNullable<ReturnType<typeof useSecurity>['data']>['db']; now: number }) {
  const badge = dbBadge(db, now)
  return (
    <div className={styles.meta}>
      <Pill kind={badge.kind}>{badge.text}</Pill>
    </div>
  )
}
