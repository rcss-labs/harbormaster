import { useAudit } from '../../../api/hooks'
import type { AuditEntryDTO } from '../../../api/types'
import { Feed, FeedRow } from '../../../components/Feed'
import { usePagedCursor } from '../../../components/usePagedCursor'
import { useAuth } from '../../auth/AuthProvider'
import styles from './audit.module.css'

/** Sign-ins and admin changes, newest first, with "Load more" for older pages. */
export function Audit() {
  const { me } = useAuth()
  const isAdmin = me?.admin ?? false
  // Non-admins would get 403s, so the audit query stays disabled for them.
  const paged = usePagedCursor<AuditEntryDTO>('audit')
  const page = useAudit(paged.cursor, { enabled: isAdmin })

  if (!me || !isAdmin) return null

  const entries = [...paged.earlier, ...(page.data?.items ?? [])]
  const next = page.data?.next

  return (
    <>
      <div className={styles.pageHead}>
        <div className={styles.titles}>
          <h2>Audit log</h2>
          <div className={styles.meta}>
            Sign-ins and every change to users, permissions and tags. Service actions are in each service's operations log.
          </div>
        </div>
      </div>
      <div className={styles.card}>
        <Feed>
          {entries.map((entry) => (
            <FeedRow key={entry.id} ts={entry.ts} source="harbormaster" actor={entry.actor}>
              {entry.summary}
            </FeedRow>
          ))}
        </Feed>
      </div>
      {next && (
        <div className={styles.more}>
          <button type="button" className={styles.btn} onClick={() => paged.loadMore(page.data?.items ?? [], next)}>
            Load more
          </button>
        </div>
      )}
    </>
  )
}
