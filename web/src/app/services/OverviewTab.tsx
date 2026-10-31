import { Link } from 'react-router'
import type { DeploymentRecordDTO, ServiceDTO } from '../../api/types'
import { Donut } from '../../charts/Donut'
import { Legend } from '../../charts/Legend'
import { LineChart } from '../../charts/LineChart'
import { Timeline, type TimelineEvent } from '../../charts/Timeline'
import { XL72 } from '../../charts/format'
import { useNow } from '../../components/Ago'
import { Card } from '../../components/Card'
import { FeedRow, Feed } from '../../components/Feed'
import { KV } from '../../components/KV'
import { Slots } from '../../components/Slots'
import { Tile } from '../../components/Tile'
import { RolloutCard } from './overview/RolloutCard'
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

export function OverviewTab({ service }: { service: ServiceDTO }) {
  const base = `/services/${service.id}`
  const pending = Math.max(0, service.desired - service.running)
  const now = useNow(60_000)
  const taskDefinition = service.task_definition
  const networking = service.networking
  const loadBalancer = networking.load_balancers[0]
  const efs = taskDefinition.efs_mounts[0]

  return (
    <>
      <div className={`${styles.grid} ${styles.g4}`}>
        <Tile k="Tasks" v={service.running} small={`running of ${service.desired} · ${pending} pending`}>
          <Slots slots={service.slots} desired={service.desired} />
        </Tile>
        <Tile k="Deployed revision" v={<span className={styles.mono}>:{service.revision}</span>}>
          <div className={styles.tileSub}>
            <span className={styles.mono}>{service.image_tag}</span>
            <Link className={styles.accentLink} to={`${base}/task-definition`}>
              View →
            </Link>
          </div>
        </Tile>
      </div>

      <Card title="Task health">
        <Donut
          segments={[
            { name: 'Running', v: service.running, color: 'var(--success)' },
            { name: 'Missing tasks', v: pending, color: 'var(--danger)' },
          ]}
          label={`${service.running}/${service.desired}`}
          sub="running"
        />
        <div className={styles.afterDonut}>
          <RolloutCard
            serviceName={service.name}
            deployments={service.deployments}
            desiredCount={service.desired}
            strategyText={service.strategy_text}
            circuitBreakerRollback={service.circuit_breaker.enable && service.circuit_breaker.rollback}
          />
        </div>
      </Card>

      <div className={`${styles.grid} ${styles.gWide}`}>
        <Card title="Running tasks" right={`${service.running_tasks.length} running`}>
          {service.running_tasks.length ? (
            <div className={styles.taskRows}>
              <div className={`${styles.taskRow} ${styles.muted} ${styles.small}`}>
                <span>Task</span>
                <span>Zone</span>
              </div>
              {service.running_tasks.map((task) => (
                <div key={task.id} className={styles.taskRow}>
                  <Link className={styles.mono} to={`${base}/tasks`} title={task.id}>
                    {task.short_id}
                  </Link>
                  <span className={styles.muted}>
                    {task.az}
                    {task.spot ? ' · spot' : ''}
                  </span>
                </div>
              ))}
            </div>
          ) : (
            <div className={styles.empty}>No running tasks.</div>
          )}
        </Card>
        <Card title="Running task count, 72 h" right={`desired ${service.desired}`}>
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
        </Card>
      </div>

      <Card title="Deployments, 14 days">
        <Legend
          items={[
            { name: 'Deploy', color: DEPLOY_COLOR },
            { name: 'Restart', color: RESTART_COLOR },
            { name: 'Failed, rolled back', color: FAILED_COLOR },
          ]}
        />
        <Timeline events={service.deployments_14d.map(timelineEvent)} start={now - FOURTEEN_DAYS_MS} end={now} />
      </Card>

      <div className={`${styles.grid} ${styles.g2}`}>
        <Card title="Networking">
          <KV
            rows={[
              ['VPC', <span className={styles.muted}>—</span>],
              [
                'Subnets',
                networking.subnets.map((subnet) => `${subnet.id} (${plural(subnet.tasks, 'task')})`).join(', '),
              ],
              ['Security groups', <span className={styles.mono}>{networking.security_groups.join(', ')}</span>],
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
        </Card>
        <Card title="Task definition">
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
              ['Task role', <span className={`${styles.mono} ${styles.small}`}>{imageTail(taskDefinition.task_role_arn)}</span>],
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
        </Card>
      </div>

      <Card
        title="Recent events"
        right={
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
      </Card>
    </>
  )
}
