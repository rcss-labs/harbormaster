import { Link } from 'react-router'
import type { NotificationDelivery } from '../../../api/notifications'
import { EmptyState } from '../../../components/EmptyState'
import { Panel } from '../../../components/Panel'
import { Pill } from '../../../components/Pill'
import { SegmentedFilter } from '../../../components/Seg'
import { destinationLabel, deliveryNote, eventLabel, statusPill } from './notifications'
import styles from './notifications.module.css'

interface DeliveryLogProps {
  status: 'all' | 'failed'
  onStatus: (status: 'all' | 'failed') => void
  failedCount: number
  rows: NotificationDelivery[]
  loading: boolean
  error: string | null
  hasMore: boolean
  loadingMore: boolean
  onLoadMore: () => void
  onRetry: (id: number) => void
  retrying: boolean
  phone: boolean
}

/** Where a row's alert points: its service, its cluster (an aggregated cluster row), or its account. */
function alertLink(row: NotificationDelivery): string | null {
  if (row.service_id) return `/services/${row.service_id}/overview`
  if (row.scope === 'cluster' && row.cluster_id) return `/clusters/${row.cluster_id}`
  if (row.scope === 'account' && row.account_id) return `/?account=${encodeURIComponent(row.account_id)}${row.region ? `&region=${encodeURIComponent(row.region)}` : ''}`
  return null
}

const when = (iso: string) => new Date(iso).toLocaleString()

function AlertCell({ row }: { row: NotificationDelivery }) {
  const link = alertLink(row)
  return (
    <>
      {link ? <Link to={link}>{row.alert}</Link> : <span>{row.alert}</span>}
      {(row.event === 'update' || row.event === 'diagnosis') && <span className={styles.tag}>{eventLabel(row.event)}</span>}
    </>
  )
}

function ResultCell({ row, onRetry, retrying }: { row: NotificationDelivery; onRetry: (id: number) => void; retrying: boolean }) {
  const pill = statusPill(row)
  const note = deliveryNote(row)
  return (
    <span className={styles.result}>
      <Pill kind={pill.kind}>{pill.label}</Pill>
      {note && <span className={`${styles.muted} ${styles.small}`}>{note}</span>}
      {row.can_retry && (
        <button type="button" className={`${styles.btn} ${styles.sm}`} disabled={retrying} onClick={() => onRetry(row.id)}>
          Retry
        </button>
      )}
    </span>
  )
}

export function DeliveryLog(props: DeliveryLogProps) {
  const { status, rows } = props
  return (
    <Panel
      title="Delivery log"
      flush
      loading={props.loading}
      error={props.error ? { message: 'Could not load the delivery log.', detail: props.error } : undefined}
      toolbar={
        <SegmentedFilter
          label="Show deliveries"
          value={status}
          onChange={props.onStatus}
          options={[
            { value: 'all', label: 'All' },
            { value: 'failed', label: 'Failed', count: props.failedCount },
          ]}
        />
      }
    >
      {rows.length === 0 ? (
        <EmptyState>{status === 'failed' ? 'No failed deliveries.' : 'Nothing has been sent yet.'}</EmptyState>
      ) : props.phone ? (
        <ul className={styles.logCards} aria-label="Deliveries">
          {rows.map((row) => (
            <li key={row.id} className={styles.logCard}>
              <div className={styles.logTop}>
                <span className={`${styles.muted} ${styles.small}`}>{when(row.at)}</span>
                <ResultCell row={row} onRetry={props.onRetry} retrying={props.retrying} />
              </div>
              <div>
                <AlertCell row={row} />
              </div>
              <div className={`${styles.muted} ${styles.small}`}>{`${row.route} · ${destinationLabel(row.destination)}`}</div>
            </li>
          ))}
        </ul>
      ) : (
        <table className={styles.logTable} aria-label="Deliveries">
          <thead>
            <tr>
              <th>Time</th>
              <th>Alert</th>
              <th>Route</th>
              <th>Result</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((row) => (
              <tr key={row.id}>
                <td className={`${styles.muted} ${styles.small}`}>{when(row.at)}</td>
                <td>
                  <AlertCell row={row} />
                </td>
                <td>
                  <div>{row.route}</div>
                  <div className={`${styles.muted} ${styles.small}`}>{`${destinationLabel(row.destination)}: ${row.target}`}</div>
                </td>
                <td>
                  <ResultCell row={row} onRetry={props.onRetry} retrying={props.retrying} />
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {props.hasMore && (
        <div className={styles.more}>
          <button type="button" className={styles.btn} disabled={props.loadingMore} onClick={props.onLoadMore}>
            Load more
          </button>
        </div>
      )}
    </Panel>
  )
}
