import { changesLabel, type PendingRow } from './changeSummary'
import styles from './PendingChangesAside.module.css'

interface PendingChangesAsideProps {
  /** Number of change sections (definition, service, scaling) the change set will carry. */
  count: number
  rows: PendingRow[]
  problems: string[]
  /** Set while the draft started from `?revision=N`. */
  loadedRevision?: { text: string; dismissLabel: string; onDismiss: () => void }
  discardDisabled: boolean
  reviewDisabled: boolean
  onDiscard: () => void
  onReview: () => void
}

/** Sticky summary beside the Deploy form: each edited field as "label: before → after", then Discard and Review. */
export function PendingChangesAside({ count, rows, problems, loadedRevision, discardDisabled, reviewDisabled, onDiscard, onReview }: PendingChangesAsideProps) {
  const groups = [...new Set(rows.map((row) => row.group))]
  return (
    // An explicit role keeps the landmark a named region, as the footer it replaces was.
    <aside role="region" aria-label="Pending changes" className={styles.aside}>
      <header className={styles.head}>
        <h2 className={styles.count}>{changesLabel(count)}</h2>
      </header>
      <div className={styles.body}>
        {loadedRevision && (
          <p className={styles.note}>
            <span>{loadedRevision.text}</span>
            <button type="button" className={styles.link} onClick={loadedRevision.onDismiss}>
              {loadedRevision.dismissLabel}
            </button>
          </p>
        )}
        {rows.length === 0 && <p className={styles.empty}>Edit a field to see it here.</p>}
        {groups.map((group) => (
          <div key={group} className={styles.group}>
            <p className={styles.groupTitle}>{group}</p>
            <ul className={styles.rows}>
              {rows
                .filter((row) => row.group === group)
                .map((row) => (
                  <li key={row.label} aria-label={row.label} className={styles.row}>
                    <span className={styles.label}>{row.label}</span>
                    <span aria-hidden="true">: </span>
                    <span className={styles.before}>{row.before}</span>
                    <span aria-hidden="true"> → </span>
                    <span className={styles.after}>{row.after}</span>
                  </li>
                ))}
            </ul>
          </div>
        ))}
        {problems.length > 0 && (
          <ul className={styles.problems} aria-label="Problems">
            {problems.map((problem) => (
              <li key={problem}>{problem}</li>
            ))}
          </ul>
        )}
      </div>
      <footer className={styles.actions}>
        <button type="button" className={styles.btn} disabled={discardDisabled} onClick={onDiscard}>
          Discard
        </button>
        <button type="button" className={`${styles.btn} ${styles.primary}`} disabled={reviewDisabled} onClick={onReview}>
          Review…
        </button>
      </footer>
    </aside>
  )
}
