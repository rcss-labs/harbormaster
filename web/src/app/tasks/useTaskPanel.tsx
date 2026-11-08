import { useState, type ReactNode } from 'react'
import type { TaskDTO } from '../../api/types'
import { StopTaskDialog } from '../../components/StopTaskDialog'
import { TaskDrawer } from './TaskDrawer'

/**
 * Drawer and Stop dialog state for a task list. The drawer follows the task by id, so a refetch updates what it
 * shows. Stop from the drawer closes it first: the confirm dialog and the drawer would otherwise fight over focus
 * and Esc.
 */
export function useTaskPanel(tasks: TaskDTO[], desiredCount: number | null = null): { onOpen(task: TaskDTO): void; onStop(task: TaskDTO): void; panel: ReactNode } {
  const [opened, setOpened] = useState<TaskDTO | null>(null)
  const [stopping, setStopping] = useState<TaskDTO | null>(null)
  const current = opened && (tasks.find((task) => task.id === opened.id) ?? opened)

  const onStop = (task: TaskDTO) => {
    setOpened(null)
    setStopping(task)
  }
  const panel = (
    <>
      {current && <TaskDrawer task={current} onClose={() => setOpened(null)} onStop={onStop} />}
      {stopping && (
        <StopTaskDialog
          taskId={stopping.id}
          shortId={stopping.short_id.slice(0, 12)}
          serviceName={stopping.service?.name ?? null}
          serviceId={stopping.service?.id ?? null}
          desiredCount={desiredCount}
          onClose={() => setStopping(null)}
        />
      )}
    </>
  )
  return { onOpen: setOpened, onStop, panel }
}
