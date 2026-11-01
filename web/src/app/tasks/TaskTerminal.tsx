import { Link, useParams } from 'react-router'
import { useTasks } from '../../api/hooks'
import { Banner } from '../../components/Banner'
import { EmptyState } from '../../components/EmptyState'
import { useCrumbs } from '../shell/crumbs'
import { TerminalPanel } from '../services/terminal/TerminalTab'

/** Terminal for a standalone task (no service page to host the tab). Permission and exec flag come from the task row. */
export function TaskTerminal() {
  const { shortId = '' } = useParams()
  useCrumbs([['Tasks', '/tasks'], [shortId.slice(0, 12)], ['Terminal']])
  const tasks = useTasks({ filter: 'running', q: shortId })
  if (!tasks.data) return null
  const task = tasks.data.items.find((item) => item.short_id === shortId && !item.service)
  if (!task) return <EmptyState>This task is not running.</EmptyState>
  if (!task.can.terminal) {
    return <Banner kind="warn">You need <strong>Terminal</strong> on this cluster&apos;s tag to use this. Ask a global admin to change your permissions.</Banner>
  }
  if (!task.exec_enabled) {
    return (
      <Banner kind="warn">
        <strong>This task was started without ECS Exec.</strong> Run it with <code>--enable-execute-command</code> and give
        the task role the <code>ssmmessages:*</code> permissions. <Link to="/tasks">Back to tasks</Link>
      </Banner>
    )
  }
  return <TerminalPanel running={[task]} />
}
