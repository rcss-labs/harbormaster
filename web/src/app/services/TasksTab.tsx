import type { TaskDTO } from '../../api/types'
import { useDesiredCountInfo } from '../../api/change'
import { useServiceTasks } from '../../api/hooks'
import { EmptyState } from '../../components/EmptyState'
import { TaskTable } from '../../components/TaskTable'
import { usePagedCursor } from '../../components/usePagedCursor'

/** The service's tasks, newest first, with "Load more" for further pages. */
export function TasksTab({ serviceId }: { serviceId: string }) {
  const paged = usePagedCursor<TaskDTO>(serviceId)
  const page = useServiceTasks(serviceId, paged.cursor)
  const desired = useDesiredCountInfo(serviceId)

  if (page.isError) {
    return <EmptyState>{page.error instanceof Error ? page.error.message : 'The tasks could not be loaded.'}</EmptyState>
  }

  const next = page.data?.next ?? undefined
  return (
    <TaskTable
      items={[...paged.earlier, ...(page.data?.items ?? [])]}
      withService={false}
      next={next}
      desiredCount={desired.data?.desired ?? null}
      onMore={() => {
        if (page.data?.next) paged.loadMore(page.data.items, page.data.next)
      }}
    />
  )
}
