import type { ChangeSetResponse } from '../../../api/deploy'
import { Banner } from '../../../components/Banner'
import { Dialog } from '../../../components/Dialog'
import { diffLines } from '../task-definition/diffLines'
import diff from '../task-definition/taskDefinition.module.css'
import { REASON_MAX_LENGTH, primaryLabel, summarySentence, type ChangeSections, type ServiceRow } from './changeSummary'
import styles from './ReviewChangesDialog.module.css'

interface ReviewChangesDialogProps {
  serviceName: string
  tagName: string
  deployedRevision: number
  predictedRevision: number
  sections: ChangeSections
  /** The deployed definition and what the change set would leave deployed, both as pretty JSON. */
  definitionBefore: string
  definitionAfter: string
  serviceRows: ServiceRow[]
  scalingLines: string[]
  canDeploy: boolean
  reason: string
  busy: boolean
  error?: string
  /** The revision the service was on when editing started, when it has moved since. */
  movedFrom: number | null
  settingsChanged: boolean
  /** Set after a partial run: the dialog then shows the steps and offers to retry the rest. */
  partial: ChangeSetResponse | null
  onReasonChange(reason: string): void
  onApply(): void
  onRetry(): void
  onClose(): void
}

const STEP_LABEL = { register: 'Register revision', service: 'Update service', scaling: 'Update auto scaling' } as const
const STATUS_LABEL = { done: 'Done', failed: 'Failed', skipped: 'Skipped' } as const

export function ReviewChangesDialog(props: ReviewChangesDialogProps) {
  const { sections, partial, busy } = props
  const rowClass = { '+': diff.add, '-': diff.del, gap: diff.gap, ' ': '' }
  const label = primaryLabel({ sections, predictedRevision: props.predictedRevision, canDeploy: props.canDeploy })

  return (
    <Dialog
      wide
      title={`Review changes to ${props.serviceName}`}
      sub={summarySentence({ serviceName: props.serviceName, sections, predictedRevision: props.predictedRevision, canDeploy: props.canDeploy })}
      onClose={props.onClose}
      actions={[
        { label: partial ? 'Close' : 'Keep editing', onClick: props.onClose },
        partial
          ? { label: 'Retry remaining steps', kind: 'primary', onClick: props.onRetry, disabled: busy }
          : { label, kind: 'primary', onClick: props.onApply, disabled: busy },
      ]}
    >
      {props.tagName === 'Prod' && <Banner kind="warn">This is a Prod service.</Banner>}
      {props.movedFrom !== null && (
        <Banner kind="warn">{`The service moved from :${props.movedFrom} to :${props.deployedRevision} while you were editing. Check the diff before applying.`}</Banner>
      )}
      {props.settingsChanged && (
        <Banner kind="warn">
          <strong>Settings changed while you were editing</strong> The left-hand values are the current ones; check them before applying.
        </Banner>
      )}

      {partial && (
        <section aria-label="Steps" className={styles.section}>
          <h3 className={styles.heading}>What happened</h3>
          <ol className={styles.steps}>
            {partial.steps.map((step) => (
              <li key={step.name} className={styles.step} data-status={step.status}>
                <span className={styles.stepName}>{STEP_LABEL[step.name]}</span>
                <span className={styles.status}>{STATUS_LABEL[step.status]}</span>
                <span className={styles.stepMessage}>{step.message}</span>
              </li>
            ))}
          </ol>
        </section>
      )}

      {sections.target && (
        <section aria-label="Task definition changes" className={styles.section}>
          <h3 className={styles.heading}>
            {sections.target.kind === 'revision'
              ? `Task definition: :${props.deployedRevision} → :${sections.target.revision}`
              : `Task definition: new revision :${props.predictedRevision}`}
          </h3>
          <div className={diff.diff}>
            {diffLines(props.definitionBefore, props.definitionAfter).map((row, index) => (
              <div key={index} className={rowClass[row.kind]}>
                {row.kind === 'gap' ? row.text : `${row.kind} ${row.text}`}
              </div>
            ))}
          </div>
        </section>
      )}

      {props.serviceRows.length > 0 && (
        <section aria-label="Service changes" className={styles.section}>
          <h3 className={styles.heading}>Service</h3>
          <dl className={styles.rows}>
            {props.serviceRows.map((row) => (
              <div key={row.label} className={styles.row}>
                <dt>{row.label}</dt>
                <dd>
                  <span className={styles.before}>{row.before}</span>
                  <span aria-hidden="true" className={styles.arrow}>→</span>
                  <span className={styles.after}>{row.after}</span>
                </dd>
              </div>
            ))}
          </dl>
        </section>
      )}

      {props.scalingLines.length > 0 && (
        <section aria-label="Auto scaling changes" className={styles.section}>
          <h3 className={styles.heading}>Auto scaling</h3>
          <ul className={styles.lines}>
            {props.scalingLines.map((line) => (
              <li key={line}>{line}</li>
            ))}
          </ul>
        </section>
      )}

      {!partial && (
        <label className={styles.reason}>
          <span className={styles.heading}>Reason</span>
          <textarea
            aria-label="Reason"
            rows={2}
            maxLength={REASON_MAX_LENGTH}
            placeholder="Optional. Shown in the operations log."
            value={props.reason}
            onChange={(event) => props.onReasonChange(event.target.value)}
          />
          <span className={styles.counter}>{`${props.reason.length} / ${REASON_MAX_LENGTH}`}</span>
        </label>
      )}
      {props.error && <Banner kind="bad">{props.error}</Banner>}
    </Dialog>
  )
}
