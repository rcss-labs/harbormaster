import { Banner } from '../../../components/Banner'
import { Dialog } from '../../../components/Dialog'
import type { ScanGateView } from '../deploy/ReviewChangesDialog'
import { ScanGatePanel } from '../deploy/ScanGatePanel'
import { diffLines } from './diffLines'
import styles from './taskDefinition.module.css'

interface ReviewDialogProps {
  serviceName: string
  deployedRevision: number
  predictedRevision: number
  baseRevision: number
  before: string
  after: string
  registering: boolean
  error?: string
  onKeepEditing(): void
  onRegister(): void
  /** Set while the pre-deploy scan gate waits for the user: the dialog then offers Deploy anyway and Cancel. */
  scanGate?: ScanGateView
}

export function ReviewDialog(props: ReviewDialogProps) {
  const { serviceName, deployedRevision, predictedRevision, baseRevision, registering, error, scanGate } = props
  const rows = diffLines(props.before, props.after)
  const rowClass = { '+': styles.add, '-': styles.del, gap: styles.gap, ' ': '' }

  return (
    <Dialog
      wide
      title={`Register ${serviceName}:${predictedRevision}?`}
      sub={`Changes against the deployed revision :${deployedRevision}.`}
      onClose={props.onKeepEditing}
      actions={
        scanGate
          ? [
              { label: 'Cancel', onClick: scanGate.onCancel },
              { label: 'Deploy anyway', kind: 'primary', onClick: scanGate.onDeployAnyway, disabled: registering },
            ]
          : [
              { label: 'Keep editing', onClick: props.onKeepEditing },
              {
                label: `Register revision :${predictedRevision}`,
                kind: 'primary',
                onClick: props.onRegister,
                disabled: registering,
              },
            ]
      }
    >
      {scanGate && <ScanGatePanel serviceId={scanGate.serviceId} gate={scanGate.gate} onRowSettled={scanGate.onRowSettled} />}
      {baseRevision !== deployedRevision && (
        <Banner kind="warn">
          {`The service moved from :${baseRevision} to :${deployedRevision} while you were editing. Check the diff before saving.`}
        </Banner>
      )}
      <div className={styles.diff}>
        {rows.map((row, index) => (
          <div key={index} className={rowClass[row.kind]}>
            {row.kind === 'gap' ? row.text : `${row.kind} ${row.text}`}
          </div>
        ))}
      </div>
      {error && <Banner kind="bad">{error}</Banner>}
    </Dialog>
  )
}
