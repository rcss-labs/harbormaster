import { useEffect } from 'react'
import { Link } from 'react-router'
import { useScanStatus, type ScanGate, type ScanGateImage } from '../../../api/security'
import { isPendingGateRow } from './useGatedSubmit'
import styles from './scan.module.css'

interface ScanGatePanelProps {
  serviceId: string
  gate: ScanGate
  /** A row that was waiting for its scan has stopped moving. */
  onRowSettled(container: string): void
}

const PROGRESS_LABEL: Partial<Record<string, string>> = {
  queued: 'Queued',
  scanning: 'Scanning…',
  refreshing_db: 'Updating vulnerability data…',
  scanned: 'Scanned',
}

function GateRow({ serviceId, row, onSettled }: { serviceId: string; row: ScanGateImage; onSettled(container: string): void }) {
  const waiting = isPendingGateRow(row)
  // By digest: a digest-pinned image or a changed repository has no tag to ask by.
  const status = useScanStatus(serviceId, { digest: row.digest }, waiting)
  const polled = status.data?.state
  // A cached answer from an earlier look is not news: the row settles on a read made while it is on screen.
  const settled = waiting && status.isFetchedAfterMount && polled !== undefined && polled !== 'queued' && polled !== 'scanning' && polled !== 'refreshing_db'
  useEffect(() => {
    if (settled) onSettled(row.container)
  }, [settled, onSettled, row.container])

  let detail
  if (waiting) detail = PROGRESS_LABEL[polled ?? row.state] ?? PROGRESS_LABEL[row.state]
  else if (row.state === 'pass') detail = 'No critical findings.'
  else if (row.state === 'critical') {
    detail = (
      <>
        {`${row.open.critical} critical, ${row.open.high} high, ${row.open.medium} medium`}
        {row.digest !== '' && (
          <>
            {' · '}
            <Link to={`/security?image=${row.digest}`}>Open findings</Link>
          </>
        )}
      </>
    )
  } else detail = row.message

  return (
    <li className={styles.gateRow} data-state={row.state}>
      <span className={styles.gateContainer}>{row.container}</span>
      <span className={styles.gateImage}>{row.image}</span>
      <span className={styles.gateDetail}>{detail}</span>
    </li>
  )
}

/** One row per changed image the pre-deploy gate paused on. Rows whose scan is still moving are followed until they settle. */
// Each 409 gets its own id so its rows start over: a repeated row for the same container and digest must be read and settled again.
const gateIds = new WeakMap<ScanGate, number>()
let lastGateId = 0
const gateIdOf = (gate: ScanGate): number => {
  if (!gateIds.has(gate)) gateIds.set(gate, ++lastGateId)
  return gateIds.get(gate) as number
}

export function ScanGatePanel({ serviceId, gate, onRowSettled }: ScanGatePanelProps) {
  return (
    <section aria-label="Image scan" className={styles.gate}>
      <h3 className={styles.gateHeading}>Image scan</h3>
      <p className={styles.gateBody}>A scan never blocks a deploy; critical findings pause it until you choose.</p>
      <ul className={styles.gateRows}>
        {gate.images.map((row) => (
          <GateRow key={`${gateIdOf(gate)}:${row.container}:${row.ref}`} serviceId={serviceId} row={row} onSettled={onRowSettled} />
        ))}
      </ul>
    </section>
  )
}
