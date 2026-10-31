import type { TaskDTO } from '../../api/types'
import { useServiceTasks } from '../../api/hooks'
import { useServiceMetrics } from '../../api/observe'
import { EmptyState } from '../../components/EmptyState'
import { TaskTable } from '../../components/TaskTable'
import { usePagedCursor } from '../../components/usePagedCursor'

/** The service's tasks, newest first, with "Load more" for further pages. */
export function TasksTab({ serviceId }: { serviceId: string }) {
  const paged = usePagedCursor<TaskDTO>(serviceId)
  const page = useServiceTasks(serviceId, paged.cursor)
  const taskUsage = useServiceMetrics(serviceId, '1h').data?.tasks_now
  // CloudWatch reports the ECS task id, which is the task's short_id.
  const utilisation = taskUsage && Object.fromEntries(taskUsage.map((usage) => [usage.task_id, { cpu: usage.cpu, mem: usage.mem }]))

  if (page.isError) {
    return <EmptyState>{page.error instanceof Error ? page.error.message : 'The tasks could not be loaded.'}</EmptyState>
  }

  const next = page.data?.next ?? undefined
  return (
    <TaskTable
      items={[...paged.earlier, ...(page.data?.items ?? [])]}
      withService={false}
      utilisation={utilisation ?? undefined}
      next={next}
      onMore={() => {
        if (page.data?.next) paged.loadMore(page.data.items, page.data.next)
      }}
    />
  )
}
