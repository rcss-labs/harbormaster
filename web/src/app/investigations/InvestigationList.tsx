import type { InvestigationDTO } from '../../api/investigations'
import { Panel } from '../../components/Panel'
import { formatEventTime } from '../services/timeline/model'
import { stateText } from './stateText'
import styles from './investigations.module.css'

/** What the list shows of a summary: its first line, as text. */
export const firstLine = (summary: string): string => {
  const end = summary.indexOf('\n')
  return end === -1 ? summary : summary.slice(0, end)
}

/** A service's investigations, newest first as the API sends them; choosing one calls `onOpen`. */
export function InvestigationList({ items, withDate, onOpen }: { items: InvestigationDTO[]; withDate: boolean; onOpen: (id: number) => void }) {
  return (
    <Panel title="Investigations" flush>
      <div className={styles.list}>
        {items.map((item) => (
          <div key={item.id} className={styles.row}>
            <span className={styles.time}>{formatEventTime(item.requested_at, withDate)}</span>
            <button type="button" className={styles.rowButton} onClick={() => onOpen(item.id)}>
              {stateText(item.state, item.reason)}
            </button>
            <span className={styles.firstLine}>{firstLine(item.summary)}</span>
          </div>
        ))}
      </div>
    </Panel>
  )
}
