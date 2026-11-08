import type { ReactNode } from 'react'
import { Link } from 'react-router'
import { useServiceMetrics } from '../../api/observe'
import type { DeploymentRecordDTO, ServiceDTO } from '../../api/types'
import { StackBar } from '../../components/StackBar'
import { DataBanner } from '../../components/DataBanner'
import { Legend } from '../../charts/Legend'
import { LineChart } from '../../charts/LineChart'
import { Timeline, type TimelineEvent } from '../../charts/Timeline'
import { XL72 } from '../../charts/format'
import { useNow } from '../../components/Ago'
import { Panel } from '../../components/Panel'
import { FeedRow, Feed } from '../../components/Feed'
import { KV } from '../../components/KV'
import { ALBPanels, HealthCard, RunningTasksMeters, ServiceKpis, UsageChart } from './overview/MetricPanels'
import { RolloutActions } from './overview/RolloutActions'
import { usePhone } from '../../components/usePhone'
import { RolloutCard } from './overview/RolloutCard'
import { queryPanelError } from './panelError'
import styles from './service.module.css'

const FOURTEEN_DAYS_MS = 14 * 86_400_000
const RECENT_EVENT_COUNT = 6

const DEPLOY_COLOR = 'var(--accent)'
const RESTART_COLOR = 'var(--series-3)'
const FAILED_COLOR = 'var(--danger)'

const imageTail = (reference: string) => reference.split('/').pop() ?? reference
const plural = (count: number, noun: string) => `${count} ${noun}${count === 1 ? '' : 's'}`

function timelineEvent(record: DeploymentRecordDTO): TimelineEvent {
  const failed = record.outcome === 'failed' || record.outcome === 'rolled_back'
  const name = record.kind === 'restart' ? 'restart' : `:${record.revision}`
  return {
    ts: Date.parse(record.started_at),
    label: failed ? `${name} failed` : name,
    color: failed ? FAILED_COLOR : record.kind === 'restart' ? RESTART_COLOR : DEPLOY_COLOR,
  }
}

function TaskHealth({ service, pending, children }: { service: ServiceDTO; pending: number; children?: ReactNode }) {
  return (
    <Panel title="Task health" sub={<span className={styles.mono}>{`${service.running}/${service.desired} running`}</span>}>
      <StackBar
        label="Task health"
        total={Math.max(service.desired, service.running)}
        legend
        parts={[
          { label: 'Running', n: service.running, color: 'var(--ok)' },
          { label: 'Missing tasks', n: pending, color: 'var(--bad)' },
        ]}
      />
      {children && <div className={styles.afterBar}>{children}</div>}
    </Panel>
  )
}

export function OverviewTab({ service }: { service: ServiceDTO }) {
  const base = `/services/${service.id}`
  const phone = usePhone()
  const pending = Math.max(0, service.desired - service.running)
  const now = useNow(60_000)
  const metricsQuery = useServiceMetrics(service.id, '24h')
  const metrics = metricsQuery.data
  const taskDefinition = service.task_definition
  const networking = service.networking
  const vpc = networking.vpc
  const securityGroupLabel = (groupId: string) => {
    const groupName = networking.security_group_names[groupId]
    return groupName ? `${groupId} (${groupName})` : groupId
  }
  const loadBalancer = networking.load_balancers[0]
  const efs = taskDefinition.efs_mounts[0]
  const rollout = (
    <RolloutCard
      serviceName={service.name}
      deployments={service.deployments}
      desiredCount={service.desired}
      strategyText={service.strategy_text}
      circuitBreakerEnable={service.circuit_breaker.enable}
      circuitBreakerRollback={service.circuit_breaker.rollback}
    />
  )

  return (
    <>
      <DataBanner tiers={['Target health', 'Tasks']} />
      <ServiceKpis service={service} metrics={metrics} />

      <div className={`${styles.grid} ${styles.gWide}`}>
        <UsageChart
          service={service}
          metrics={metrics}
          loading={metricsQuery.isPending}
          error={queryPanelError(metricsQuery, 'CPU and memory could not be read.')}
        />
        {phone ? (
          <>
            <Panel title="Rollout">
              {rollout}
              <RolloutActions service={service} />
            </Panel>
            {loadBalancer ? <HealthCard service={service} /> : <TaskHealth service={service} pending={pending} />}
          </>
        ) : loadBalancer ? (
          <HealthCard service={service}>{rollout}</HealthCard>
        ) : (
          <TaskHealth service={service} pending={pending}>
            {rollout}
          </TaskHealth>
        )}
      </div>

      <ALBPanels service={service} metrics={metrics} />

      <div className={`${styles.grid} ${styles.gWide}`}>
        <RunningTasksMeters service={service} metrics={metrics} />
        <Panel title="Running task count, 72 h" actions={`desired ${service.desired}`}>
          <LineChart
            series={[
              { name: 'running', color: 'var(--accent)', values: service.counts_history_72h.map((point) => point.running) },
              { name: 'desired', color: 'var(--border-strong)', values: service.counts_history_72h.map((point) => point.desired) },
            ]}
            unit=""
            max={Math.max(2, service.desired + 1)}
            xLabels={XL72}
            height={110}
          />
        </Panel>
      </div>

      <Panel title="Deployments, 14 days">
        <Legend
          items={[
            { name: 'Deploy', color: DEPLOY_COLOR },
            { name: 'Restart', color: RESTART_COLOR },
            { name: 'Failed, rolled back', color: FAILED_COLOR },
          ]}
        />
        <Timeline events={service.deployments_14d.map(timelineEvent)} start={now - FOURTEEN_DAYS_MS} end={now} />
      </Panel>

      <div className={`${styles.grid} ${styles.g2}`}>
        <Panel
          title="Networking"
          actions={
            vpc ? (
              <Link className={styles.accentLink} to={`/networks/${service.account_id}/${service.region}/${vpc.vpc_id}`}>
                Network placement →
              </Link>
            ) : undefined
          }
        >
          <KV
            rows={[
              [
                'VPC',
                vpc ? (
                  <>
                    {vpc.name} <span className={`${styles.mono} ${styles.muted}`}>{vpc.vpc_id}</span>
                  </>
                ) : (
                  <span className={styles.muted}>—</span>
                ),
              ],
              [
                'Subnets',
                networking.subnets.map((subnet) => `${subnet.id} (${plural(subnet.tasks, 'task')})`).join(', '),
              ],
              [
                'Security groups',
                <span className={styles.mono}>{networking.security_groups.map(securityGroupLabel).join(', ')}</span>,
              ],
              ['Public IP', networking.assign_public_ip ? 'Assigned' : 'Not assigned'],
              [
                'Load balancer',
                loadBalancer ? (
                  `${loadBalancer.target_group_name} → port ${loadBalancer.container_port}`
                ) : (
                  <span className={styles.muted}>None</span>
                ),
              ],
              ['Service Connect', <span className={styles.muted}>Off</span>],
            ]}
          />
        </Panel>
        <Panel title="Task definition">
          <KV
            rows={[
              [
                'Family',
                <span className={styles.mono}>
                  {taskDefinition.family}:{taskDefinition.revision}
                </span>,
              ],
              ['Size', `${(Number(taskDefinition.cpu) / 1024).toFixed(2)} vCPU · ${taskDefinition.memory} MiB`],
              [
                'Containers',
                taskDefinition.containers.map((container) => (
                  <div key={container.name} className={`${styles.mono} ${styles.small}`}>
                    {container.name} · {imageTail(container.image)}
                  </div>
                )),
              ],
              [
                'Task role',
                taskDefinition.task_role_arn ? (
                  <span className={`${styles.mono} ${styles.small}`}>{imageTail(taskDefinition.task_role_arn)}</span>
                ) : (
                  <span className={styles.muted}>None</span>
                ),
              ],
              [
                'ECS Exec',
                service.enable_execute_command ? 'On' : <span className={styles.warning}>Off</span>,
              ],
              [
                'EFS',
                efs ? (
                  <>
                    {efs.file_system_id} → <span className={styles.mono}>{efs.container_path}</span>
                  </>
                ) : (
                  <span className={styles.muted}>None</span>
                ),
              ],
            ]}
          />
        </Panel>
      </div>

      <Panel
        title="Recent events"
        flush
        actions={
          <Link className={styles.accentLink} to={`${base}/operations`}>
            Operations log →
          </Link>
        }
      >
        <Feed>
          {service.recent_events.slice(0, RECENT_EVENT_COUNT).map((event, index) => (
            <FeedRow key={`${event.ts}-${index}`} ts={event.ts} source={event.source} actor={event.actor}>
              {event.text}
            </FeedRow>
          ))}
        </Feed>
      </Panel>
    </>
  )
}
