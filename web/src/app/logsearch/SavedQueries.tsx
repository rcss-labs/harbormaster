import type { SavedLogSearchDTO } from '../../api/logSearch'
import styles from './logsearch.module.css'

interface SavedQueriesProps {
  items: SavedLogSearchDTO[]
  onLoad: (saved: SavedLogSearchDTO) => void
  onDelete: (saved: SavedLogSearchDTO) => void
}

/** The list inside the Saved queries panel (desktop) or sheet (phone). */
export function SavedQueries({ items, onLoad, onDelete }: SavedQueriesProps) {
  if (items.length === 0) return <p className={styles.hint}>No saved queries yet.</p>
  return (
    <ul className={styles.saved}>
      {items.map((saved) => {
        const count = saved.service_ids.length
        return (
          <li key={saved.id} className={styles.savedRow}>
            <span className={styles.savedName}>{saved.name}</span>
            <span className={styles.savedMeta}>{`${count} ${count === 1 ? 'service' : 'services'}`}</span>
            <span className={styles.savedActions}>
              <button type="button" className={`${styles.btn} ${styles.small}`} aria-label={`Load ${saved.name}`} onClick={() => onLoad(saved)}>
                Load
              </button>
              <button type="button" className={`${styles.btn} ${styles.small}`} aria-label={`Delete ${saved.name}`} onClick={() => onDelete(saved)}>
                Delete
              </button>
            </span>
          </li>
        )
      })}
    </ul>
  )
}
