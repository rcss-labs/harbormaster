import { useState } from 'react'
import type { EventDTO } from '../../api/types'
import { useOperations } from '../../api/hooks'
import { EmptyState } from '../../components/EmptyState'
import { Feed, FeedRow } from '../../components/Feed'
import { usePagedCursor } from '../../components/usePagedCursor'
import styles from './service.module.css'

/** The service's operations log: Harbormaster actions and, optionally, ECS service events. */
export function OperationsTab({ serviceId }: { serviceId: string }) {
  const [includeEcs, setIncludeEcs] = useState(true)
  const paged = usePagedCursor<EventDTO>(`${serviceId}|${includeEcs}`)
  const page = useOperations(serviceId, includeEcs, paged.cursor)

  if (page.isError) {
    return <EmptyState>{page.error instanceof Error ? page.error.message : 'The operations log could not be loaded.'}</EmptyState>
  }

  const items = [...paged.earlier, ...(page.data?.items ?? [])]
  const next = page.data?.next

  return (
    <>
      <div className={styles.opsToolbar}>
        <label className={styles.toggle}>
          <input type="checkbox" checked={includeEcs} onChange={(event) => setIncludeEcs(event.target.checked)} />
          Show ECS service events
        </label>
        <span className={`${styles.muted} ${styles.small} ${styles.end}`}>
          Actions taken in Harbormaster are kept for 90 days. Changes made outside Harbormaster come from CloudTrail.
        </span>
      </div>
      <div className={styles.panel}>
        {items.length === 0 ? (
          <EmptyState>No entries.</EmptyState>
        ) : (
          <Feed>
            {items.map((event, index) => (
              <FeedRow key={index} ts={event.ts} source={event.source} actor={event.actor}>
                {event.text}
              </FeedRow>
            ))}
          </Feed>
        )}
        {next && (
          <div className={styles.more}>
            <button
              type="button"
              className={styles.btn}
              onClick={() => {
                if (page.data?.next) paged.loadMore(page.data.items, page.data.next)
              }}
            >
              Load more
            </button>
          </div>
        )}
      </div>
    </>
  )
}
