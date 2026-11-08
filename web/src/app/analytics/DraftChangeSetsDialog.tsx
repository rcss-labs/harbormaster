import { Dialog } from '../../components/Dialog'
import { Pill } from '../../components/Pill'
import { rightsizeHref } from '../services/deploy/rightsizeParams'
import type { SuggestedService } from './cost'
import styles from './analytics.module.css'

interface DraftChangeSetsDialogProps {
  rows: SuggestedService[]
  headroomPct: number
  /** Services whose link was followed this session. */
  opened: ReadonlySet<string>
  onOpened: (serviceId: string) => void
  onClose: () => void
}

/** One link per selected service into the normal Deploy review. Nothing is posted from here. */
export function DraftChangeSetsDialog({ rows, headroomPct, opened, onOpened, onClose }: DraftChangeSetsDialogProps) {
  return (
    <Dialog sub="CPU units, memory MiB" title={`Draft ${rows.length} change ${rows.length === 1 ? 'set' : 'sets'}`} wide onClose={onClose} actions={[{ label: 'Close', onClick: onClose }]}>
      <p className={styles.draftNote}>
        {`Nothing deploys yet. Each link opens that service's Deploy tab with the size filled in and the reason "Rightsizing at ${headroomPct}% headroom". Nothing is saved until you review and apply it there.`}
      </p>
      <ul className={styles.draftList}>
        {rows.map(({ service, suggestion }) => (
          <li key={service.service_id} className={styles.draftRow}>
            <span className={styles.draftName}>{service.service}</span>
            <span className={styles.dimMono}>{`Now ${service.cpu_units} / ${service.memory_mib}`}</span>
            <span className={styles.mono}>{`Suggested ${suggestion.cpu_units} / ${suggestion.memory_mib}`}</span>
            <a
              href={rightsizeHref(service.service_id, { cpuUnits: suggestion.cpu_units, memoryMib: suggestion.memory_mib }, headroomPct)}
              target="_blank"
              rel="noopener"
              onClick={() => onOpened(service.service_id)}
            >
              Open in Deploy
            </a>
            {opened.has(service.service_id) && <Pill kind="ok">Opened</Pill>}
          </li>
        ))}
      </ul>
    </Dialog>
  )
}
