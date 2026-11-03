import { useState } from 'react'
import { useTaskDefinition } from '../../../api/change'
import { useDeploySettings } from '../../../api/deploy'
import { useService, useServiceTasks } from '../../../api/hooks'
import { EmptyState } from '../../../components/EmptyState'
import { Panel } from '../../../components/Panel'
import { oomMemoryHint, type Definition } from '../task-definition/mainContainer'
import { ContainersPanel } from './ContainersPanel'
import { ScalingPanel } from './ScalingPanel'
import { ServicePanel } from './ServicePanel'
import { TaskSizePanel } from './TaskSizePanel'
import { definitionErrors, isDefinitionChanged } from './draft'
import { scalingDraftFromSettings, scalingDraftToChange, type ScalingDraft } from './scalingDraft'
import { serviceDraftFromSettings, serviceDraftToChange, type ServiceDraft } from './serviceDraft'
import styles from './DeployTab.module.css'

/**
 * The Deploy tab. Today it shows the live settings read-only; the editors for each panel
 * (containers, task size, service, auto scaling) replace the bodies in later tasks.
 */
export function DeployTab({ serviceId }: { serviceId: string }) {
  const settingsQuery = useDeploySettings(serviceId)
  const taskDefinition = useTaskDefinition(serviceId)
  const service = useService(serviceId)
  // Null until the user edits: the draft then follows the live settings (including a refetch after a write).
  const [editedService, setEditedService] = useState<ServiceDraft | null>(null)
  const [editedScaling, setEditedScaling] = useState<ScalingDraft | null>(null)
  const [editedDefinition, setEditedDefinition] = useState<Definition | null>(null)
  const stoppedTasks = useServiceTasks(serviceId)

  if (settingsQuery.isError) {
    return <EmptyState>{settingsQuery.error instanceof Error ? settingsQuery.error.message : 'The deploy settings could not be loaded.'}</EmptyState>
  }
  const settings = settingsQuery.data
  if (!settings) return null

  const liveDefinition = taskDefinition.data?.definition
  // Null until the user edits: the draft then follows the current task definition (including a refetch after a write).
  const definitionDraft = editedDefinition ?? liveDefinition
  const serviceDraft = editedService ?? serviceDraftFromSettings(settings)
  const scalingDraft = editedScaling ?? scalingDraftFromSettings(settings)
  const tagName = service.data?.tag.name ?? 'this service'
  const canEditDefinition = (taskDefinition.data?.can ?? settings.can).edit
  const definitionChanged = definitionDraft !== undefined && liveDefinition !== undefined && isDefinitionChanged(definitionDraft, liveDefinition)
  const definitionProblems = definitionDraft ? definitionErrors(definitionDraft) : []
  const oomHint = definitionDraft
    ? oomMemoryHint({
        stoppedForOom: stoppedTasks.data?.items.some((task) => task.status === 'STOPPED' && task.stopped_reason.includes('OutOfMemoryError')) ?? false,
        deployedMemory: liveDefinition?.memory,
        draftMemory: definitionDraft.memory,
        draftCpu: definitionDraft.cpu,
      })
    : undefined
  // Sections with edits, for the review footer added later. `definitionDraft`, `definitionChanged` and `definitionProblems`
  // are what the review step registers (the draft is the full register-ready definition).
  const changes =
    [serviceDraftToChange(settings, serviceDraft), scalingDraftToChange(settings, scalingDraft)].filter(Boolean).length + (definitionChanged ? 1 : 0)

  return (
    <div className={styles.grid} data-changes={changes} data-definition-changed={definitionChanged} data-definition-problems={definitionProblems.length}>
      {definitionDraft && liveDefinition ? (
        <>
          <ContainersPanel
            serviceId={serviceId}
            definition={definitionDraft}
            deployed={liveDefinition}
            onChange={setEditedDefinition}
            canEdit={canEditDefinition}
            isAdmin={settings.can.admin}
            tagName={tagName}
          />
          <TaskSizePanel definition={definitionDraft} onChange={setEditedDefinition} canEdit={canEditDefinition} tagName={tagName} oomHint={oomHint} />
        </>
      ) : (
        <>
          <Panel title="Containers">
            <p className={styles.mono}>{taskDefinition.isError ? 'The task definition could not be loaded.' : 'Loading…'}</p>
          </Panel>
          <Panel title="Task size">
            <p className={styles.mono}>{taskDefinition.isError ? 'The task definition could not be loaded.' : 'Loading…'}</p>
          </Panel>
        </>
      )}
      <ServicePanel settings={settings} draft={serviceDraft} onChange={setEditedService} tagName={tagName} />
      <ScalingPanel settings={settings} draft={scalingDraft} onChange={setEditedScaling} desiredCount={serviceDraft.desiredCount} tagName={tagName} />
    </div>
  )
}
