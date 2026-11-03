import { useQueryClient } from '@tanstack/react-query'
import { useEffect, useState } from 'react'
import { useSearchParams } from 'react-router'
import { isApiError, useRevisions, useTaskDefinition } from '../../../api/change'
import {
  useChangeSet,
  useDeploySettings,
  useRevisionDefinition,
  type ChangeSetRequest,
  type ChangeSetResponse,
  type DeploySettingsResponse,
} from '../../../api/deploy'
import { useService, useServiceTasks } from '../../../api/hooks'
import { EmptyState } from '../../../components/EmptyState'
import { Panel } from '../../../components/Panel'
import { showWarning, useToast } from '../../../components/Toast'
import { oomMemoryHint, type Definition } from '../task-definition/mainContainer'
import { ContainersPanel } from './ContainersPanel'
import { RolloutProgress, shouldShowProgress } from './RolloutProgress'
import { ReviewChangesDialog } from './ReviewChangesDialog'
import { ScalingPanel } from './ScalingPanel'
import { ServicePanel } from './ServicePanel'
import { TaskSizePanel } from './TaskSizePanel'
import { buildChangeSetRequest, buildRetryRequest, changesLabel, countSections, scalingLines, serviceRows, type ChangeSections } from './changeSummary'
import { definitionErrors, isDefinitionChanged } from './draft'
import { rebaseScalingDraft, rebaseServiceDraft } from './rebase'
import { scalingDraftErrors, scalingDraftFromSettings, scalingDraftToChange, type ScalingDraft } from './scalingDraft'
import { serviceDraftErrors, serviceDraftFromSettings, serviceDraftToChange, type ServiceDraft } from './serviceDraft'
import styles from './DeployTab.module.css'

/** What the drafts were based on when the user started editing; the change set is sent against it. */
interface EditBase {
  revision: number
  version: string
}

/** A change set that stopped half way: the request that was sent and what the server did with it. */
interface PartialRun {
  request: ChangeSetRequest
  response: ChangeSetResponse
}

interface ConflictBody {
  settings?: DeploySettingsResponse
  deployed_revision?: number
}

const pretty = (definition: Definition) => JSON.stringify(definition, null, 2)

function parseRevisionParam(value: string | null): number | null {
  if (value === null || !/^[1-9]\d*$/.test(value)) return null
  return Number(value)
}

/**
 * The Deploy tab: editors for the task definition, service settings and auto scaling, and a sticky footer that reviews
 * every pending edit as one change set.
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
  const revisions = useRevisions(serviceId)
  const changeSet = useChangeSet(serviceId)
  const queryClient = useQueryClient()
  const toast = useToast()
  const [searchParams, setSearchParams] = useSearchParams()
  const revisionParam = parseRevisionParam(searchParams.get('revision'))
  const revisionTarget = useRevisionDefinition(serviceId, revisionParam)
  const [editBase, setEditBase] = useState<EditBase | null>(null)
  const [reviewing, setReviewing] = useState(false)
  const [reason, setReason] = useState('')
  const [submitError, setSubmitError] = useState<string | undefined>()
  const [movedFrom, setMovedFrom] = useState<number | null>(null)
  const [settingsChanged, setSettingsChanged] = useState(false)
  const [partial, setPartial] = useState<PartialRun | null>(null)
  // Ticks so a finished rollout stays visible for a minute and then disappears.
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), 5000)
    return () => clearInterval(timer)
  }, [])

  if (settingsQuery.isError) {
    return <EmptyState>{settingsQuery.error instanceof Error ? settingsQuery.error.message : 'The deploy settings could not be loaded.'}</EmptyState>
  }
  const settings = settingsQuery.data
  if (!settings) return null

  const liveDefinition = taskDefinition.data?.definition
  const deployedRevision = taskDefinition.data?.deployed_revision ?? settings.deployed_revision
  // A revision from ?revision=N is the starting point of the definition draft until the user edits it.
  const revisionPending = revisionParam !== null && revisionParam !== deployedRevision
  const preloaded = revisionPending ? (revisionTarget.data?.definition as Definition | undefined) : undefined
  // Null until the user edits: the draft then follows the current task definition (including a refetch after a write).
  const definitionDraft = editedDefinition ?? preloaded ?? liveDefinition
  const serviceDraft = editedService ?? serviceDraftFromSettings(settings)
  const scalingDraft = editedScaling ?? scalingDraftFromSettings(settings)
  const tagName = service.data?.tag.name ?? 'this service'
  const serviceName = service.data?.name ?? 'this service'
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

  const sections: ChangeSections = {
    target: !definitionChanged || !definitionDraft ? null : editedDefinition ? { kind: 'definition', definition: editedDefinition } : preloaded ? { kind: 'revision', revision: revisionParam as number } : null,
    service: serviceDraftToChange(settings, serviceDraft),
    scaling: scalingDraftToChange(settings, scalingDraft),
  }
  const changes = countSections(sections)
  const problems = [...definitionProblems, ...serviceDraftErrors(settings, serviceDraft), ...scalingDraftErrors(settings, scalingDraft)]
  const predictedRevision = Math.max(deployedRevision, ...(revisions.data?.items.map((item) => item.revision) ?? [])) + 1
  const baseline: EditBase = { revision: deployedRevision, version: settings.version }
  const base = editBase ?? baseline

  // Remember what the first edit was based on, so a change made by someone else in the meantime is caught on submit.
  const edit =
    <T,>(setter: (value: T) => void) =>
    (value: T) => {
      setEditBase((current) => current ?? baseline)
      setter(value)
    }

  const clearRevisionParam = () => {
    if (searchParams.has('revision')) setSearchParams((previous) => { previous.delete('revision'); return previous }, { replace: true })
  }

  const resetDrafts = () => {
    setEditedService(null)
    setEditedScaling(null)
    setEditedDefinition(null)
    setEditBase(null)
    setPartial(null)
    setReason('')
    setReviewing(false)
    setSubmitError(undefined)
    setMovedFrom(null)
    setSettingsChanged(false)
    clearRevisionParam()
  }

  // Takes fresh settings as the new baseline; the user's edits stay where they still differ from what is live now.
  const adoptSettings = (fresh: DeploySettingsResponse) => {
    queryClient.setQueryData(['service', serviceId, 'deploy-settings'], fresh)
    if (editedService) setEditedService(rebaseServiceDraft(editedService, settings, fresh))
    if (editedScaling) setEditedScaling(rebaseScalingDraft(editedScaling, settings))
    setEditBase((current) => ({ revision: current?.revision ?? baseline.revision, version: fresh.version }))
  }

  const onFinished = (response: ChangeSetResponse) => {
    if (response.status === 'registered_only') {
      const registered = response.registered_revision === null ? '' : ` :${response.registered_revision}`
      toast.show(`Registered${registered}. Someone with Deploy on ${tagName} can roll it out.`, 'ok')
    } else {
      toast.show(response.to_revision === null ? `Applied the changes to ${serviceName}.` : `Deploying ${serviceName} to :${response.to_revision}.`, 'ok')
    }
    resetDrafts()
  }

  const onFailed = async (failure: unknown) => {
    const body = isApiError(failure, 'conflict') ? (failure.body as ConflictBody | undefined) : undefined
    if (body?.settings) {
      adoptSettings(body.settings)
      setSettingsChanged(true)
    } else if (typeof body?.deployed_revision === 'number') {
      const [, freshSettings] = await Promise.all([taskDefinition.refetch(), settingsQuery.refetch()])
      if (freshSettings.data) adoptSettings(freshSettings.data)
      setMovedFrom(base.revision)
      const movedTo = body.deployed_revision
      setEditBase((current) => ({ revision: movedTo, version: current?.version ?? baseline.version }))
    } else {
      setSubmitError(failure instanceof Error ? failure.message : 'The changes could not be applied.')
    }
  }

  const submit = async (request: ChangeSetRequest) => {
    setSubmitError(undefined)
    try {
      const response = await changeSet.mutateAsync(request)
      showWarning(toast, response.warning)
      if (response.status === 'partial') setPartial({ request, response })
      else onFinished(response)
    } catch (failure) {
      await onFailed(failure)
    }
  }

  const apply = () => void submit(buildChangeSetRequest({ baseRevision: base.revision, baseVersion: base.version, reason, sections }))
  const retry = () => {
    const next = partial && buildRetryRequest(partial.request, partial.response)
    if (next) void submit(next)
  }
  const reviewable = (changes > 0 || partial !== null) && problems.length === 0

  return (
    <>
      {service.data && shouldShowProgress(service.data, now) && (
        <RolloutProgress serviceId={serviceId} service={service.data} canDeploy={service.data.can.deploy} tagName={tagName} />
      )}
      <div className={styles.grid} data-changes={changes} data-definition-changed={definitionChanged} data-definition-problems={definitionProblems.length}>
        {definitionDraft && liveDefinition ? (
          <>
            <ContainersPanel
              serviceId={serviceId}
              definition={definitionDraft}
              deployed={liveDefinition}
              onChange={edit(setEditedDefinition)}
              canEdit={canEditDefinition}
              isAdmin={settings.can.admin}
              tagName={tagName}
            />
            <TaskSizePanel definition={definitionDraft} onChange={edit(setEditedDefinition)} canEdit={canEditDefinition} tagName={tagName} oomHint={oomHint} />
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
        <ServicePanel settings={settings} draft={serviceDraft} onChange={edit(setEditedService)} tagName={tagName} />
        <ScalingPanel settings={settings} draft={scalingDraft} onChange={edit(setEditedScaling)} desiredCount={serviceDraft.desiredCount} tagName={tagName} />
      </div>

      <section className={styles.footer} aria-label="Pending changes">
        <div className={styles.footerInfo}>
          <span className={styles.count}>{changesLabel(changes)}</span>
          {revisionPending && (
            <span className={styles.note}>
              <span>{revisionTarget.isError ? `Revision :${revisionParam} could not be loaded` : revisionTarget.data ? `:${revisionParam} is loaded from the revisions list` : `Loading :${revisionParam}…`}</span>
              <button type="button" className={styles.link} onClick={clearRevisionParam}>{`Dismiss :${revisionParam}`}</button>
            </span>
          )}
          {problems.length > 0 && (
            <ul className={styles.problems} aria-label="Problems">
              {problems.map((problem) => (
                <li key={problem}>{problem}</li>
              ))}
            </ul>
          )}
        </div>
        <div className={styles.footerActions}>
          <button type="button" className={styles.btn} disabled={changes === 0 && partial === null} onClick={resetDrafts}>
            Discard
          </button>
          <button
            type="button"
            className={`${styles.btn} ${styles.primary}`}
            disabled={!reviewable}
            onClick={() => {
              setEditBase((current) => current ?? baseline)
              setReviewing(true)
            }}
          >
            Review…
          </button>
        </div>
      </section>

      {reviewing && (
        <ReviewChangesDialog
          serviceName={serviceName}
          tagName={tagName}
          deployedRevision={deployedRevision}
          predictedRevision={predictedRevision}
          sections={sections}
          definitionBefore={liveDefinition ? pretty(liveDefinition) : ''}
          definitionAfter={definitionDraft ? pretty(definitionDraft) : ''}
          serviceRows={sections.service ? serviceRows(settings, sections.service) : []}
          scalingLines={sections.scaling ? scalingLines(settings, sections.scaling) : []}
          canDeploy={settings.can.deploy}
          reason={reason}
          busy={changeSet.isPending}
          error={submitError}
          movedFrom={movedFrom}
          settingsChanged={settingsChanged}
          partial={partial?.response ?? null}
          onReasonChange={setReason}
          onApply={apply}
          onRetry={retry}
          onClose={() => {
            setReviewing(false)
            setSubmitError(undefined)
          }}
        />
      )}
    </>
  )
}
