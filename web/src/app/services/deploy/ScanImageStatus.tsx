import { Link } from 'react-router'
import { useContainerImageTags } from '../../../api/deploy'
import { useRequestScan, useScanStatus, useSecurityService, type ScanStatus } from '../../../api/security'
import { Ago } from '../../../components/Ago'
import { Banner } from '../../../components/Banner'
import form from './editors.module.css'
import styles from './scan.module.css'

interface ScanImageStatusProps {
  serviceId: string
  container: string
  /** The tag in the draft, which is the one that gets scanned. */
  tag: string
  canDeploy: boolean
  /** Name of the service's tag, for the "You need Deploy on ..." lock title. */
  tagName: string
}

const ACTIVE_LABEL = { queued: 'Queued', scanning: 'Scanning…', refreshing_db: 'Updating vulnerability data…' } as const

function Result({ status, onScanAgain, busy }: { status: ScanStatus; onScanAgain(): void; busy: boolean }) {
  const { state, image } = status
  if (state === 'queued' || state === 'scanning' || state === 'refreshing_db') return <p className={styles.statusLine}>{ACTIVE_LABEL[state]}</p>
  if (state === 'scanned' && image) {
    return (
      <>
        <p className={styles.statusLine}>
          Scanned {image.scanned_at && <Ago ts={image.scanned_at} />}: {image.open.critical} critical, {image.open.high} high, {image.open.medium} medium ·{' '}
          <Link to={`/security?image=${image.digest}`}>Open Security</Link>
        </p>
        {image.open.critical > 0 && <Banner kind="bad">{`This image has ${image.open.critical} critical findings.`}</Banner>}
      </>
    )
  }
  if (state === 'failed' || state === 'db_stale' || state === 'unsupported') {
    return (
      <p className={`${styles.statusLine} ${state === 'failed' ? styles.statusFailed : ''}`.trim()}>
        {status.message || image?.failure_action}{' '}
        {state === 'failed' && (
          <button type="button" className={form.btn} disabled={busy} onClick={onScanAgain}>
            Scan again
          </button>
        )}
      </p>
    )
  }
  return null
}

/** "Scan for vulnerabilities" and the latest scan result for the selected container's tag. Hidden while image scanning is off. */
export function ScanImageStatus({ serviceId, container, tag, canDeploy, tagName }: ScanImageStatusProps) {
  const security = useSecurityService(serviceId)
  const enabled = security.data?.enabled === true
  const tags = useContainerImageTags(serviceId, container, '')
  const status = useScanStatus(serviceId, { container, tag }, enabled && tag !== '')
  const requestScan = useRequestScan(serviceId)
  if (!enabled) return null

  const unsupported = tags.data?.source === 'none'
  const lock = unsupported ? 'Only Amazon ECR private images are scanned in this version.' : canDeploy ? undefined : `You need Deploy on ${tagName}`
  const state = status.data?.state
  const running = state === 'queued' || state === 'scanning' || state === 'refreshing_db'
  const scan = () => requestScan.mutate({ container, tag })

  return (
    <div className={styles.status}>
      <span title={lock}>
        <button type="button" className={form.btn} disabled={lock !== undefined || tag === '' || running || requestScan.isPending} onClick={scan}>
          Scan for vulnerabilities
        </button>
      </span>
      {requestScan.isError && <p className={`${styles.statusLine} ${styles.statusFailed}`}>{requestScan.error.message}</p>}
      {status.data && <Result status={status.data} onScanAgain={scan} busy={requestScan.isPending} />}
    </div>
  )
}
