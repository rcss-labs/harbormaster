import { Dialog } from '../../components/Dialog'
import { Pill } from '../../components/Pill'
import styles from './analytics.module.css'

/** One service of a draft: what it is now, what is proposed, and the Deploy tab link that fills the change in. */
export interface DraftRow {
  serviceId: string
  name: string
  now: string
  proposed: string
  href: string
}

interface DraftChangeSetsDialogProps {
  title: string
  /** What the two columns show, e.g. "CPU units, memory MiB". */
  sub?: string
  /** The sentence at the top: what the links fill in and the reason they carry. */
  note: string
  rows: DraftRow[]
  /** Services whose link was followed this session. */
  opened: ReadonlySet<string>
  onOpened: (serviceId: string) => void
  onClose: () => void
}

/** The note shared by every advisor: only what is filled in and the reason change. */
export const draftNote = (what: string, reason: string) =>
  `Nothing deploys yet. Each link opens that service's Deploy tab with ${what} filled in and the reason "${reason}". Nothing is saved until you review and apply it there.`

/** One link per selected service into the normal Deploy review. Nothing is posted from here. */
export function DraftChangeSetsDialog({ title, sub, note, rows, opened, onOpened, onClose }: DraftChangeSetsDialogProps) {
  return (
    <Dialog sub={sub} title={title} wide onClose={onClose} actions={[{ label: 'Close', onClick: onClose }]}>
      <p className={styles.draftNote}>{note}</p>
      <ul className={styles.draftList}>
        {rows.map((row) => (
          <li key={row.serviceId} className={styles.draftRow}>
            <span className={styles.draftName}>{row.name}</span>
            <span className={styles.dimMono}>{`Now ${row.now}`}</span>
            <span className={styles.mono}>{`Suggested ${row.proposed}`}</span>
            <a href={row.href} target="_blank" rel="noopener" onClick={() => onOpened(row.serviceId)}>
              Open in Deploy
            </a>
            {opened.has(row.serviceId) && <Pill kind="ok">Opened</Pill>}
          </li>
        ))}
      </ul>
    </Dialog>
  )
}
