import { useState } from 'react'
import { Navigate, useParams } from 'react-router'
import { ApiError } from '../../api/client'
import { useCluster, useService } from '../../api/hooks'
import { EmptyState } from '../../components/EmptyState'
import { EditTagDialog } from '../admin/tags/EditTagDialog'
import { LockPanel } from '../../components/LockPanel'
import { Tabs, type TabItem } from '../../components/Tabs'
import { useAuth } from '../auth/AuthProvider'
import type { TagTarget } from '../clusters/tagTarget'
import { useCrumbs } from '../shell/crumbs'
import { OperationsTab } from './OperationsTab'
import { OverviewTab } from './OverviewTab'
import { PlaceholderTab } from './PlaceholderTab'
import { ServiceHeader } from './ServiceHeader'
import { TasksTab } from './TasksTab'
import { TaskDefinitionTab } from './task-definition/TaskDefinitionTab'
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

  const items: TabItem[] = TABS.map(([key, label]) => {
    const lock = TAB_LOCKS[key]
    const locked = lock && !service.can[lock.can] ? { action: lock.action, tag: service.tag.name } : undefined
    return { key, label, locked, badge: key === 'tasks' ? service.running : undefined }
  })
  const activeLock = TAB_LOCKS[tab]
  const lockedOut = activeLock !== undefined && !service.can[activeLock.can]

  let body
  if (lockedOut) body = <LockPanel action={activeLock.action} tag={service.tag} />
  else if (tab === 'overview') body = <OverviewTab service={service} />
  else if (tab === 'tasks') body = <TasksTab serviceId={serviceId} />
  else if (tab === 'operations') body = <OperationsTab serviceId={serviceId} />
  else if (tab === 'task-definition') body = <TaskDefinitionTab key={serviceId} serviceId={serviceId} service={service} />
  else body = <PlaceholderTab owner="02" />

  return (
    <>
      <ServiceHeader service={service} onTagClick={me?.admin ? setEditing : undefined} />
      <Tabs items={items} active={tab} hrefFor={(key) => `/services/${serviceId}/${key}`} />
      {body}
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
