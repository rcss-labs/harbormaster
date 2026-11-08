import { useState } from 'react'
import { Link } from 'react-router'
import type { LogSearchResultDTO } from '../../api/logSearch'
import { usePhone } from '../../components/usePhone'
import { logsTabHref } from './model'
import styles from './logsearch.module.css'

const PAGE_SIZE = 200

/** `2026-11-08T10:00:03.250Z` becomes `10:00:03.250` (UTC). */
const lineTime = (iso: string) => iso.slice(11, 23)

/** The result lines, 200 at a time. Each line links to the service's Logs tab at that second. */
export function ResultLines({ result }: { result: LogSearchResultDTO }) {
  const phone = usePhone()
  const [shown, setShown] = useState(PAGE_SIZE)
  const names = new Map(result.services.map((service) => [service.id, service.name]))
  const visible = result.rows.slice(0, shown)
  return (
    <>
      <ul className={styles.lines} aria-label="Matching lines" data-layout={phone ? 'cards' : 'rows'}>
        {visible.map((row, index) => {
          const service = names.get(row.service_id) ?? row.service_id
          const message = row.message_truncated ? `${row.message} …` : row.message
          return (
            <li key={index}>
              <Link to={logsTabHref(row)} className={styles.line} title={`${row.t} · ${row.container}`}>
                {phone ? (
                  <>
                    <span className={styles.lineMeta} data-testid="line-meta">
                      <span className={styles.lineTime}>{lineTime(row.t)}</span>
                      <span className={styles.lineService}>{service}</span>
                    </span>
                    <span className={styles.lineMessage} data-testid="line-message">
                      {message}
                    </span>
                  </>
                ) : (
                  <>
                    <span className={styles.lineTime}>{lineTime(row.t)}</span>
                    <span className={styles.lineService}>{service}</span>
                    <span className={styles.lineMessage}>{message}</span>
                  </>
                )}
              </Link>
            </li>
          )
        })}
      </ul>
      {result.rows.length > shown && (
        <div className={styles.more}>
          <button type="button" className={`${styles.btn} ${styles.small}`} onClick={() => setShown((count) => count + PAGE_SIZE)}>
            Show more
          </button>
          <span className={styles.hint}>{`Showing ${shown} of ${result.rows.length} lines`}</span>
        </div>
      )}
    </>
  )
}
