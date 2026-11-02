import { useState } from 'react'
import { Navigate, useNavigate, useParams } from 'react-router'
import { ApiError } from '../../api/client'
import { useRevisions } from '../../api/change'
import { useCluster, useService } from '../../api/hooks'
import { EmptyState } from '../../components/EmptyState'
import { EditTagDialog } from '../admin/tags/EditTagDialog'
import { LockPanel } from '../../components/LockPanel'
import { Tabs, type TabItem } from '../../components/Tabs'
import { useAuth } from '../auth/AuthProvider'
import type { TagTarget } from '../clusters/tagTarget'
import { useCrumbs } from '../shell/crumbs'
import { ControllerBanner } from './ControllerBanner'
import { LogsTab } from './logs/LogsTab'
import { MetricsTab } from './metrics/MetricsTab'
import { isRolloutRunning } from './rollout'
import { PendingRevisionBanner } from './PendingRevisionBanner'
import { OperationsTab } from './OperationsTab'
import { OverviewTab } from './OverviewTab'
import { useRolloutToast } from './useRolloutToast'
import { PlaceholderTab } from './PlaceholderTab'
import { ScalingTab } from './scaling/ScalingTab'
import { ServiceHeader } from './ServiceHeader'
import { TasksTab } from './TasksTab'
import { TaskDefinitionTab } from './task-definition/TaskDefinitionTab'
import { TerminalTab } from './terminal/TerminalTab'
import { DEFAULT_TAB, TABS, TAB_LOCKS, isTabKey } from './tabs'
import styles from './service.module.css'

function NoAccess() {
  return (
    <div className={styles.noAccess}>
      <h2>No access</h2>
      <p>You need View on this service's tag to open this service.</p>
    </div>
  )
}

export function ServicePage() {
  const { serviceId = '', tab = DEFAULT_TAB } = useParams()
  const serviceQuery = useService(serviceId)
  const { me } = useAuth()
  const [editing, setEditing] = useState<TagTarget | null>(null)
  const service = serviceQuery.data
  const navigate = useNavigate()
  const revisions = useRevisions(serviceId)
  useRolloutToast(service)
  useCrumbs(
    service
      ? [['Clusters', '/clusters'], [service.cluster.name, `/clusters/${service.cluster.id}`], [service.name]]
      : [['Clusters', '/clusters']],
  )

  if (!isTabKey(tab)) return <Navigate to={`/services/${serviceId}/${DEFAULT_TAB}`} replace />
  if (serviceQuery.error instanceof ApiError && serviceQuery.error.status === 404) return <NoAccess />
  if (serviceQuery.isError) {
    const message = serviceQuery.error instanceof Error ? serviceQuery.error.message : 'The service could not be loaded.'
    return <EmptyState>{message}</EmptyState>
  }
  if (!service) return null

  const revisionNumbers = revisions.data?.items.map((item) => item.revision) ?? []
  const newestRevision = Math.max(0, ...revisionNumbers)
  const rolloutRunning = isRolloutRunning(service, revisions.data)
  const pendingRevision = revisions.data && newestRevision > revisions.data.deployed_revision && !rolloutRunning ? newestRevision : null

  const items: TabItem[] = TABS.map(([key, label]) => {
    const lock = TAB_LOCKS[key]
    const locked = lock && !service.can[lock.can] ? { action: lock.action, tag: service.tag.name } : undefined
    return { key, label, locked, badge: key === 'tasks' ? service.running : key === 'task-definition' && pendingRevision ? `:${pendingRevision} ready` : undefined }
  })
  const activeLock = TAB_LOCKS[tab]
  const lockedOut = activeLock !== undefined && !service.can[activeLock.can]

  let body
  if (lockedOut) body = <LockPanel action={activeLock.action} tag={service.tag} />
  else if (tab === 'overview') body = <OverviewTab service={service} />
  else if (tab === 'tasks') body = <TasksTab serviceId={serviceId} />
  else if (tab === 'operations') body = <OperationsTab serviceId={serviceId} />
  else if (tab === 'metrics') body = <MetricsTab serviceId={serviceId} />
  else if (tab === 'logs') body = <LogsTab serviceId={serviceId} />
  else if (tab === 'scaling') body = <ScalingTab serviceId={serviceId} />
  else if (tab === 'task-definition') body = <TaskDefinitionTab key={serviceId} serviceId={serviceId} service={service} />
  else if (tab === 'terminal') body = <TerminalTab serviceId={serviceId} />
  else body = <PlaceholderTab owner="02" />

  return (
    <>
      <ServiceHeader service={service} onTagClick={me?.admin ? setEditing : undefined} />
      {!service.controller.supported && <ControllerBanner banner={service.controller.banner} />}
      {pendingRevision !== null && revisions.data && (
        <PendingRevisionBanner
          serviceName={service.name}
          pendingRevision={pendingRevision}
          deployedRevision={revisions.data.deployed_revision}
          canDeploy={service.can.deploy && service.controller.supported}
          deployDisabledReason={service.can.deploy ? service.controller.banner : `You need Deploy on ${service.tag.name}`}
          onDeploy={() => navigate(`/services/${serviceId}/task-definition?deploy=${pendingRevision}`)}
        />
      )}
      <Tabs items={items} active={tab} hrefFor={(key) => `/services/${serviceId}/${key}`} />
      <div className={styles.tabPanel}>{body}</div>
      {editing && <ServiceTagEditor target={editing} clusterId={service.cluster.id} onClose={() => setEditing(null)} />}
    </>
  )
}

/** Waits for the cluster's tag, which the inherit option needs, then opens the tag dialog for the service. */
function ServiceTagEditor({ target, clusterId, onClose }: { target: TagTarget; clusterId: string; onClose: () => void }) {
  const cluster = useCluster(clusterId)
  if (!cluster.data) return null
  return (
    <EditTagDialog
      kind="service"
      id={target.id}
      name={target.name}
      current={target.tag.inherited ? null : target.tag.id}
      clusterName={cluster.data.name}
      clusterTag={cluster.data.tag}
      onClose={onClose}
    />
  )
}
