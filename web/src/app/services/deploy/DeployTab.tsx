import { useTaskDefinition } from '../../../api/change'
import { useDeploySettings, type DeploySettingsResponse } from '../../../api/deploy'
import { EmptyState } from '../../../components/EmptyState'
import { KV } from '../../../components/KV'
import { Panel } from '../../../components/Panel'
import { plural } from './format'
import styles from './DeployTab.module.css'

function describeRollout(settings: DeploySettingsResponse): string {
  const { circuit_breaker: breaker, minimum_healthy_percent: minimum, maximum_percent: maximum } = settings.deployment_configuration
  const safety = !breaker.enable ? 'circuit breaker off' : breaker.rollback ? 'circuit breaker with rollback' : 'circuit breaker without rollback'
  return `min healthy ${minimum}% · max ${maximum}% · ${safety}`
}

function describeScaling(scaling: DeploySettingsResponse['scaling']): string {
  if (!scaling.has_target) return 'No scalable target registered.'
  return `min ${scaling.min} · max ${scaling.max} · ${plural(scaling.policies.length, 'policy', 'policies')} · ${plural(scaling.scheduled_actions.length, 'scheduled action')}`
}

/**
 * The Deploy tab. Today it shows the live settings read-only; the editors for each panel
 * (containers, task size, service, auto scaling) replace the bodies in later tasks.
 */
export function DeployTab({ serviceId }: { serviceId: string }) {
  const settingsQuery = useDeploySettings(serviceId)
  const taskDefinition = useTaskDefinition(serviceId)

  if (settingsQuery.isError) {
    return <EmptyState>{settingsQuery.error instanceof Error ? settingsQuery.error.message : 'The deploy settings could not be loaded.'}</EmptyState>
  }
  const settings = settingsQuery.data
  if (!settings) return null

  const definition = taskDefinition.data?.definition
  const containers = Array.isArray(definition?.containerDefinitions)
    ? (definition.containerDefinitions as { name?: string; image?: string }[])
    : []
  const grace = settings.health_check_grace_period_seconds

  return (
    <div className={styles.grid}>
      <Panel title="Containers">
        <KV
          rows={[
            ['Task definition', <span className={styles.mono}>{`${settings.family}:${settings.deployed_revision}`}</span>],
            ...containers.map((container): [string, React.ReactNode] => [
              container.name ?? 'container',
              <span className={styles.mono}>{container.image ?? ''}</span>,
            ]),
          ]}
        />
      </Panel>
      <Panel title="Task size">
        <KV
          rows={[
            ['CPU', definition ? `${String(definition.cpu ?? '')} units` : '–'],
            ['Memory', definition ? `${String(definition.memory ?? '')} MiB` : '–'],
          ]}
        />
      </Panel>
      <Panel title="Service">
        <KV
          rows={[
            ['Desired count', `${settings.desired_count}`],
            ['Running', `${settings.running_count}`],
            ['ECS Exec', settings.enable_execute_command ? 'On' : 'Off'],
            ['Deployment', describeRollout(settings)],
            ...(settings.has_load_balancer ? [['Health check grace period', grace === null ? 'Not set' : `${grace} s`] as [string, string]] : []),
          ]}
        />
      </Panel>
      <Panel title="Auto scaling">
        <p>{describeScaling(settings.scaling)}</p>
      </Panel>
    </div>
  )
}
