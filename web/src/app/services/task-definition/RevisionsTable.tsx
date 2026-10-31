import type { RevisionDTO, RevisionsResponse } from '../../../api/types'
import { Ago } from '../../../components/Ago'
import { Pill, type PillKind } from '../../../components/Pill'
import { Table } from '../../../components/Table'
import styles from './taskDefinition.module.css'

interface RevisionsTableProps {
  serviceName: string
  revisions: RevisionsResponse
  canDeploy: boolean
  /** Tooltip for disabled actions: the lock explanation, or the controller banner. */
  deployDisabledReason?: string
  rolloutRunning: boolean
  onDeploy(revision: number, mode: 'deploy' | 'rollback'): void
}

const STATUS: Record<RevisionDTO['status'], { kind: PillKind; label: string }> = {
  deployed: { kind: 'ok', label: 'Deployed' },
  rolling_out: { kind: 'info', label: 'Rolling out' },
  not_deployed: { kind: 'mute', label: 'Not deployed' },
  previous: { kind: 'mute', label: 'Previous' },
}

export function RevisionsTable({ serviceName, revisions, canDeploy, deployDisabledReason, rolloutRunning, onDeploy }: RevisionsTableProps) {
  const disabled = !canDeploy || deployDisabledReason !== undefined || rolloutRunning
  const title = !canDeploy || deployDisabledReason !== undefined ? deployDisabledReason : rolloutRunning ? 'A rollout is already running' : undefined

  return (
    <div>
      <div className={styles.cardHead}>
        <h2>Revisions</h2>
        <span className={styles.note}>{`${revisions.items.length} shown · newest first`}</span>
      </div>
      <Table>
        <thead>
          <tr>
            <th>Revision</th>
            <th>Status</th>
            <th>Image tag</th>
            <th>CPU / memory</th>
            <th>Registered</th>
            <th>By</th>
            <th />
          </tr>
        </thead>
        <tbody>
          {revisions.items.map((item) => {
            const status = STATUS[item.status]
            const mode = item.status === 'previous' ? 'rollback' : item.status === 'not_deployed' ? 'deploy' : null
            return (
              <tr key={item.revision}>
                <td className="mono" title={`${serviceName}:${item.revision} · ${item.arn}`}>{`:${item.revision}`}</td>
                <td>
                  <Pill kind={status.kind}>{status.label}</Pill>
                </td>
                <td className="mono">{item.image_tag}</td>
                <td>{`${item.cpu} / ${item.memory}`}</td>
                <td>
                  <Ago ts={item.registered_at} />
                </td>
                <td>{item.registered_by}</td>
                <td>
                  {mode && (
                    <button
                      type="button"
                      className={styles.btn}
                      disabled={disabled}
                      title={title}
                      onClick={() => onDeploy(item.revision, mode)}
                    >
                      {mode === 'deploy' ? 'Deploy' : 'Roll back to this'}
                    </button>
                  )}
                </td>
              </tr>
            )
          })}
        </tbody>
      </Table>
    </div>
  )
}
