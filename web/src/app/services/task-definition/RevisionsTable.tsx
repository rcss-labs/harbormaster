import { useState } from 'react'
import type { RevisionDTO, RevisionsResponse } from '../../../api/types'
import { Ago } from '../../../components/Ago'
import { BottomSheet, SheetAction } from '../../../components/BottomSheet'
import { DataTable, type DataTableColumn } from '../../../components/DataTable'
import { Icon } from '../../../components/Icon'
import { Panel } from '../../../components/Panel'
import { Pill, type PillKind } from '../../../components/Pill'
import { usePhone } from '../../../components/usePhone'
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
  const phone = usePhone()
  const [sheetFor, setSheetFor] = useState<RevisionDTO | null>(null)
  const disabled = !canDeploy || deployDisabledReason !== undefined || rolloutRunning
  const title = !canDeploy || deployDisabledReason !== undefined ? deployDisabledReason : rolloutRunning ? 'A rollout is already running' : undefined
  const modeOf = (item: RevisionDTO) => (item.status === 'previous' ? 'rollback' : item.status === 'not_deployed' ? 'deploy' : null)

  const columns: DataTableColumn<RevisionDTO>[] = [
    {
      key: 'revision',
      header: 'Revision',
      width: '90px',
      render: (item) => (
        <span className="mono" title={`${serviceName}:${item.revision} · ${item.arn}`}>{`:${item.revision}`}</span>
      ),
    },
    {
      key: 'status',
      header: 'Status',
      width: '130px',
      render: (item) => <Pill kind={STATUS[item.status].kind}>{STATUS[item.status].label}</Pill>,
    },
    { key: 'image_tag', header: 'Image tag', render: (item) => <span className="mono">{item.image_tag}</span> },
    { key: 'size', header: 'CPU / memory', render: (item) => `${item.cpu} / ${item.memory}` },
    { key: 'registered', header: 'Registered', render: (item) => <Ago ts={item.registered_at} /> },
    { key: 'by', header: 'By', render: (item) => (phone ? `by ${item.registered_by}` : item.registered_by) },
    {
      key: 'actions',
      header: '',
      width: phone ? undefined : '170px',
      phone: 'action',
      render: (item) => {
        if (phone) {
          return (
            <button type="button" className={styles.moreBtn} aria-label={`Actions for revision :${item.revision}`} aria-haspopup="menu" onClick={() => setSheetFor(item)}>
              <Icon name="more" size={18} />
            </button>
          )
        }
        const mode = modeOf(item)
        return (
          mode && (
            <button type="button" className={styles.btn} disabled={disabled} title={title} onClick={() => onDeploy(item.revision, mode)}>
              {mode === 'deploy' ? 'Deploy' : 'Roll back to this'}
            </button>
          )
        )
      },
    },
  ]

  const sheetMode = sheetFor ? modeOf(sheetFor) : null
  return (
    <Panel title="Revisions" actions={<span className={styles.note}>{`${revisions.items.length} shown · newest first`}</span>} flush>
      <DataTable label="Revisions" columns={columns} rows={revisions.items} rowKey={(item) => String(item.revision)} minWidth={760} />
      {sheetFor && (
        <BottomSheet
          open
          onClose={() => setSheetFor(null)}
          role="menu"
          label={`Actions for revision :${sheetFor.revision}`}
          title={<span className="mono">{`${serviceName}:${sheetFor.revision}`}</span>}
          sub={`${STATUS[sheetFor.status].label} · ${sheetFor.image_tag} · ${sheetFor.cpu} / ${sheetFor.memory}`}
        >
          {sheetMode ? (
            <SheetAction
              icon={sheetMode === 'deploy' ? 'deploy' : 'history'}
              label={sheetMode === 'deploy' ? 'Deploy' : 'Roll back to this'}
              why={disabled ? title : undefined}
              disabled={disabled}
              onClick={() => {
                onDeploy(sheetFor.revision, sheetMode)
                setSheetFor(null)
              }}
            />
          ) : (
            <SheetAction
              icon="deploy"
              label={sheetFor.status === 'deployed' ? 'Running now' : 'Rolling out'}
              why={sheetFor.status === 'deployed' ? 'This revision is the one deployed' : 'This revision is rolling out'}
              disabled
            />
          )}
        </BottomSheet>
      )}
    </Panel>
  )
}
