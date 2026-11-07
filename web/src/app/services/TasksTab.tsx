import { useState } from 'react'
import type { TaskDTO } from '../../api/types'
import { useDesiredCountInfo } from '../../api/change'
import { useServiceTasks } from '../../api/hooks'
import { useServiceMetrics } from '../../api/observe'
import { Chips } from '../../components/Chips'
import { EmptyState } from '../../components/EmptyState'
import { Panel } from '../../components/Panel'
import { SegmentedFilter } from '../../components/Seg'
import { TaskTable } from '../../components/TaskTable'
import { usePhone } from '../../components/usePhone'
import { usePagedCursor } from '../../components/usePagedCursor'
import { HiddenStoppedNote } from '../tasks/HiddenStoppedNote'
import { useTaskPanel } from '../tasks/useTaskPanel'

type TaskSegment = 'running' | 'stopped' | 'all'

/** The service's tasks, newest first, with "Load more" for further pages. The segments filter the pages loaded so far. */
export function TasksTab({ serviceId }: { serviceId: string }) {
  const phone = usePhone()
  const paged = usePagedCursor<TaskDTO>(serviceId)
  const page = useServiceTasks(serviceId, paged.cursor)
  const taskUsage = useServiceMetrics(serviceId, '1h').data?.tasks_now
  // CloudWatch reports the ECS task id, which is the task's short_id.
  const utilisation = taskUsage && Object.fromEntries(taskUsage.map((usage) => [usage.task_id, { cpu: usage.cpu, mem: usage.mem }]))
  const desired = useDesiredCountInfo(serviceId)
  const [segment, setSegment] = useState<TaskSegment>('all')
  const loadedTasks = [...paged.earlier, ...(page.data?.items ?? [])]
  const { onOpen, onStop, panel } = useTaskPanel(loadedTasks, desired.data?.desired ?? null)

  if (page.isError) {
    return <EmptyState>{page.error instanceof Error ? page.error.message : 'The tasks could not be loadedTasks.'}</EmptyState>
  }

  const runningCount = loadedTasks.filter((task) => task.status === 'RUNNING').length
  const stoppedCount = loadedTasks.filter((task) => task.status === 'STOPPED').length
  const items = segment === 'all' ? loadedTasks : loadedTasks.filter((task) => task.status === (segment === 'running' ? 'RUNNING' : 'STOPPED'))
  const next = page.data?.next ?? undefined
  const stateOptions: { value: TaskSegment; label: string; count: number }[] = [
    { value: 'running', label: 'Running', count: runningCount },
    { value: 'stopped', label: 'Stopped', count: stoppedCount },
    { value: 'all', label: 'All', count: loadedTasks.length },
  ]
  return (
    <>
      <HiddenStoppedNote />
      {phone && <Chips label="Task state" value={segment} onChange={setSegment} options={stateOptions} />}
      <Panel
        title="Tasks"
        toolbar={phone ? undefined : <SegmentedFilter label="Task state" value={segment} onChange={setSegment} options={stateOptions} />}
        flush
      >
        <TaskTable
          items={items}
          withService={false}
          utilisation={utilisation ?? undefined}
          next={next}
          onOpen={onOpen}
          onStop={onStop}
          onMore={() => {
            if (page.data?.next) paged.loadMore(page.data.items, page.data.next)
          }}
        />
      </Panel>
      {panel}
    </>
  )
}
