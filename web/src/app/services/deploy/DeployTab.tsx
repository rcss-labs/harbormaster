import { useState } from 'react'
import { useTaskDefinition } from '../../../api/change'
import { useDeploySettings } from '../../../api/deploy'
import { useService } from '../../../api/hooks'
import { EmptyState } from '../../../components/EmptyState'
import { KV } from '../../../components/KV'
import { Panel } from '../../../components/Panel'
import { ScalingPanel } from './ScalingPanel'
import { ServicePanel } from './ServicePanel'
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

  if (settingsQuery.isError) {
    return <EmptyState>{settingsQuery.error instanceof Error ? settingsQuery.error.message : 'The deploy settings could not be loaded.'}</EmptyState>
  }
  const settings = settingsQuery.data
  if (!settings) return null

  const definition = taskDefinition.data?.definition
  const containers = Array.isArray(definition?.containerDefinitions)
    ? (definition.containerDefinitions as { name?: string; image?: string }[])
    : []
  const serviceDraft = editedService ?? serviceDraftFromSettings(settings)
  const scalingDraft = editedScaling ?? scalingDraftFromSettings(settings)
  const tagName = service.data?.tag.name ?? 'this service'
  // Sections with edits, for the review footer added later.
  const changes = [serviceDraftToChange(settings, serviceDraft), scalingDraftToChange(settings, scalingDraft)].filter(Boolean).length

  return (
    <div className={styles.grid} data-changes={changes}>
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
      <ServicePanel settings={settings} draft={serviceDraft} onChange={setEditedService} tagName={tagName} />
      <ScalingPanel settings={settings} draft={scalingDraft} onChange={setEditedScaling} desiredCount={serviceDraft.desiredCount} tagName={tagName} />
    </div>
  )
}
