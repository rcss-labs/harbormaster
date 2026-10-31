import { useQuery } from '@tanstack/react-query'
import { api } from './client'

export type ExecAgentStatus = 'RUNNING' | 'PENDING' | 'STOPPED' | 'MISSING'

export type ExecTargets = {
  task_id: string
  exec_enabled: boolean
  containers: { name: string; exec_agent: ExecAgentStatus }[]
  commands: string[]
  can_terminal: boolean
}

/** Containers and shells a task accepts for ECS Exec. Polls every 5 s unless a session is open. */
export function useExecTargets(taskId: string | undefined, connected = false) {
  return useQuery({
    queryKey: ['task', taskId, 'exec-targets'],
    queryFn: () => api.get<ExecTargets>(`/api/tasks/${taskId}/exec-targets`),
    enabled: Boolean(taskId),
    refetchInterval: connected ? false : 5000,
    refetchIntervalInBackground: false,
  })
}

/** WebSocket URL of the exec bridge, on the page's own host. */
export function execSocketUrl(taskId: string, container: string, command: string): string {
  const scheme = window.location.protocol === 'https:' ? 'wss' : 'ws'
  const query = `container=${encodeURIComponent(container)}&command=${encodeURIComponent(command)}`
  return `${scheme}://${window.location.host}/api/tasks/${encodeURIComponent(taskId)}/exec?${query}`
}
