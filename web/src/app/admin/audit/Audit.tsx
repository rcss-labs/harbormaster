import { useSearchParams } from 'react-router'
import { useAudit } from '../../../api/hooks'
import type { AuditEntryDTO } from '../../../api/types'
import { Ago } from '../../../components/Ago'
import { Chips } from '../../../components/Chips'
import { usePhone } from '../../../components/usePhone'
import { EmptyState } from '../../../components/EmptyState'
import { Feed, FeedRow } from '../../../components/Feed'
import { Panel } from '../../../components/Panel'
import { usePagedCursor } from '../../../components/usePagedCursor'
import { useAuth } from '../../auth/AuthProvider'
import styles from './audit.module.css'

const ALL_TYPES = ''

/** "permission.set" is of type "permission". */
const typeOf = (entry: AuditEntryDTO) => entry.action.split('.')[0]

const matches = (entry: AuditEntryDTO, needle: string) =>
  [entry.summary, entry.actor, entry.target, entry.action].some((field) => field.toLowerCase().includes(needle))

/** Sign-ins and admin changes, newest first, with type and text filters and "Load more" for older pages. */
export function Audit() {
  const { me } = useAuth()
  const isAdmin = me?.admin ?? false
  // Non-admins would get 403s, so the audit query stays disabled for them.
  const paged = usePagedCursor<AuditEntryDTO>('audit')
  const page = useAudit(paged.cursor, { enabled: isAdmin })
  const [params, setParams] = useSearchParams()
  const phone = usePhone()

  if (!me || !isAdmin) return null

  const entries = [...paged.earlier, ...(page.data?.items ?? [])]
  const next = page.data?.next
  const types = [...new Set(entries.map(typeOf))]
  const type = params.get('type') ?? ALL_TYPES
  const query = params.get('q') ?? ''
  const needle = query.trim().toLowerCase()
  const visible = entries.filter((entry) => (type === ALL_TYPES || typeOf(entry) === type) && (!needle || matches(entry, needle)))

  const update = (key: 'type' | 'q', value: string) =>
    setParams(
      (current) => {
        const updated = new URLSearchParams(current)
        if (value === '') updated.delete(key)
        else updated.set(key, value)
        return updated
      },
      { replace: true },
    )

  return (
    <>
      <Panel
        title="Audit log"
        flush
        toolbar={
          <>
            {phone ? (
              <Chips
                label="Type"
                options={[{ value: ALL_TYPES, label: 'All types' }, ...types.map((name) => ({ value: name, label: name }))]}
                value={type}
                onChange={(value) => update('type', value)}
              />
            ) : (
              <select className={styles.ctl} aria-label="Type" value={type} onChange={(event) => update('type', event.target.value)}>
                <option value={ALL_TYPES}>All types</option>
                {types.map((name) => (
                  <option key={name} value={name}>
                    {name}
                  </option>
                ))}
              </select>
            )}
            <input
              type="search"
              className={`${styles.ctl} ${styles.search}`}
              aria-label="Filter audit log"
              placeholder="Filter by text"
              value={query}
              onChange={(event) => update('q', event.target.value)}
            />
          </>
        }
        actions={<span title="Service actions are in each service's operations log.">Sign-ins and changes to users, permissions and tags</span>}
      >
        {visible.length === 0 ? (
          <EmptyState>
            No entries match.
            {phone && (
              <div>
                <button type="button" className={styles.btn} onClick={() => setParams(new URLSearchParams(), { replace: true })}>
                  Clear filters
                </button>
              </div>
            )}
          </EmptyState>
        ) : phone ? (
          <div className={styles.rows} role="list">
            {visible.map((entry) => (
              <div key={entry.id} role="listitem" className={styles.row}>
                <span className={styles.when}>
                  <Ago ts={entry.ts} /> · {entry.actor}
                </span>
                <span className={styles.summary}>{entry.summary}</span>
              </div>
            ))}
          </div>
        ) : (
          <Feed>
            {visible.map((entry) => (
              <FeedRow key={entry.id} ts={entry.ts} source="harbormaster" actor={entry.actor}>
                {entry.summary}
              </FeedRow>
            ))}
          </Feed>
        )}
      </Panel>
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
