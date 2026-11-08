import { useRescanImage, useSecurityImage, type ScanImageStatus } from '../../api/security'
import { formatAgo, useNow } from '../../components/Ago'
import { Pill, type PillKind } from '../../components/Pill'
import { useToast } from '../../components/Toast'
import { SCAN_STATUS_LABEL, SECURITY_COPY } from './copy'
import { FindingsTable } from './FindingsTable'
import styles from './security.module.css'

const AGO_REFRESH_MS = 15_000

export const STATUS_KIND: Record<ScanImageStatus, PillKind> = { queued: 'mute', scanning: 'info', scanned: 'ok', failed: 'bad' }

export function StatusChip({ status }: { status: ScanImageStatus }) {
  return <Pill kind={STATUS_KIND[status]}>{SCAN_STATUS_LABEL[status]}</Pill>
}

/** The body of the image detail: how it was requested and scanned, why it failed, and its findings. */
export function ImageDetail({ digest }: { digest: string }) {
  const toast = useToast()
  const detail = useSecurityImage(digest)
  const rescan = useRescanImage(digest)
  const image = detail.data
  const now = useNow(AGO_REFRESH_MS)

  if (detail.isError) return <p className={styles.empty}>{SECURITY_COPY.imageNotFound}</p>
  if (!image) return null

  const requestRescan = () =>
    rescan.mutate(undefined, {
      onSuccess: () => toast.show(SECURITY_COPY.rescanQueued, 'ok'),
      onError: (error) => toast.show(error instanceof Error ? error.message : 'The rescan could not be requested.', 'bad'),
    })

  const scanned = image.status === 'scanned'
  return (
    <div className={styles.detail}>
      <ul className={styles.facts}>
        {image.requests.map((request) => (
          <li key={`${request.service_id}|${request.container}`}>
            {SECURITY_COPY.requestedLine(request.by, request.service, request.source, formatAgo(request.at, now))}
          </li>
        ))}
        {scanned && image.scanned_at && image.matched_db_built_at && (
          <li>
            {SECURITY_COPY.scannedLine(formatAgo(image.scanned_at, now), image.syft_version, formatAgo(image.matched_db_built_at, now))}
          </li>
        )}
        {image.platform !== '' && <li>{image.platform}</li>}
        {image.packages !== null && <li>{SECURITY_COPY.packages(image.packages)}</li>}
      </ul>
      {!scanned && (
        <div className={styles.failure}>
          <StatusChip status={image.status} />
          {image.failure_action !== '' && <span>{image.failure_action}</span>}
          {image.status === 'failed' && image.can.rescan && (
            <button type="button" className={styles.btn} disabled={rescan.isPending} onClick={requestRescan}>
              {SECURITY_COPY.rescan}
            </button>
          )}
        </div>
      )}
      {scanned && image.findings.length > 0 && <FindingsTable image={image} />}
      {scanned && image.findings.length === 0 && (
        <p className={styles.empty}>{image.packages === 0 ? SECURITY_COPY.noPackages : SECURITY_COPY.noFindings}</p>
      )}
    </div>
  )
}
