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
import { Banner } from '../../../components/Banner'
import { Panel } from '../../../components/Panel'
import { usePhone } from '../../../components/usePhone'
import { showWarning, useToast } from '../../../components/Toast'
import { oomMemoryHint, type Definition } from '../task-definition/mainContainer'
import { ContainersPanel } from './ContainersPanel'
import { isRolloutRunning } from '../rollout'
import { RolloutProgress, shouldShowProgress } from './RolloutProgress'
import { ReviewChangesDialog } from './ReviewChangesDialog'
import { PendingChangesAside, PendingChangesBar } from './PendingChangesAside'
import { ScalingPanel } from './ScalingPanel'
import { ServicePanel } from './ServicePanel'
import { TaskSizePanel } from './TaskSizePanel'
import { buildChangeSetRequest, buildRetryRequest, countSections, pendingRows, scalingLines, serviceRows, type ChangeSections } from './changeSummary'
import { definitionErrors, isDefinitionChanged, setTaskSize } from './draft'
import { rebaseScalingDraft, rebaseServiceDraft } from './rebase'
import { parseRightsizeParams, RIGHTSIZE_PARAMS, rightsizeReason } from './rightsizeParams'
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

const ROLLOUT_NOTE_MAX_MS = 60_000

/**
 * The Deploy tab: editors for the task definition, service settings and auto scaling, and a sticky Pending changes aside
 * that lists each edited field before and after and reviews every pending edit as one change set.
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
  const phone = usePhone()
  const [pendingSheetOpen, setPendingSheetOpen] = useState(false)
  // Null until the user types: a suggestion from the Cost tab supplies the shown reason.
  const [reason, setReason] = useState<string | null>(null)
  const [submitError, setSubmitError] = useState<string | undefined>()
  const [movedFrom, setMovedFrom] = useState<number | null>(null)
  const [settingsChanged, setSettingsChanged] = useState(false)
  const [partial, setPartial] = useState<PartialRun | null>(null)
  // The revision an apply just started rolling out. The stored snapshot lags ECS by a poll, so for a few seconds the editors fall back to
  // the old image and no rollout shows; this note says a rollout is under way until the snapshot catches up.
  const [startedRollout, setStartedRollout] = useState<number | null>(null)
  // Ticks so a finished rollout stays visible for a minute and then disappears.
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), 5000)
    return () => clearInterval(timer)
  }, [])

  // Each part renders on its own: a settings problem degrades Service and Auto scaling, never the whole tab.
  const settings = settingsQuery.data
  const settingsError = settingsQuery.isError && !settings ? { message: settingsQuery.error instanceof Error ? settingsQuery.error.message : 'The deploy settings could not be loaded.', onRetry: () => void settingsQuery.refetch() } : undefined

  const liveDefinition = taskDefinition.data?.definition
  const deployedRevision = taskDefinition.data?.deployed_revision ?? settings?.deployed_revision
  // A revision from ?revision=N is the starting point of the definition draft until the user edits it.
  const revisionPending = revisionParam !== null && deployedRevision !== undefined && revisionParam !== deployedRevision
  const preloaded = revisionPending ? (revisionTarget.data?.definition as Definition | undefined) : undefined
  const canEditDefinition = (taskDefinition.data?.can ?? settings?.can)?.edit ?? false
  // A task size from the Cost tab (?task_cpu=...) starts the draft unless a revision was asked for; it is only a draft until Review and apply.
  const suggested = revisionParam === null && canEditDefinition ? parseRightsizeParams(searchParams) : null
  const suggestedDefinition = suggested && liveDefinition ? setTaskSize(liveDefinition, suggested) : undefined
  // Null until the user edits: the draft then follows the current task definition (including a refetch after a write).
  const definitionDraft = editedDefinition ?? preloaded ?? suggestedDefinition ?? liveDefinition
  const serviceDraft = settings ? (editedService ?? serviceDraftFromSettings(settings)) : null
  const scalingDraft = settings ? (editedScaling ?? scalingDraftFromSettings(settings)) : null
  const tagName = service.data?.tag.name ?? 'this service'
  const serviceName = service.data?.name ?? 'this service'
  const definitionChanged = definitionDraft !== undefined && liveDefinition !== undefined && isDefinitionChanged(definitionDraft, liveDefinition)
  // The Cost tab's reason is prefilled only while the draft still holds the suggested size.
  const suggestionHolds =
    suggested !== null && liveDefinition !== undefined && definitionDraft !== undefined &&
    String(definitionDraft.cpu) === suggested.cpu && String(definitionDraft.memory) === suggested.memory &&
    isDefinitionChanged(setTaskSize(liveDefinition, suggested), liveDefinition)
  const shownReason = reason ?? (suggested && suggestionHolds ? rightsizeReason(suggested.headroomPct) : '')
  // A revision (rollback) is never blocked by the client-side container floor check.
  const definitionProblems = definitionDraft ? definitionErrors(definitionDraft, revisionParam === null ? liveDefinition : undefined) : []
  const oomHint = definitionDraft
    ? oomMemoryHint({
        stoppedForOom: stoppedTasks.data?.items.some((task) => task.status === 'STOPPED' && task.stopped_reason.includes('OutOfMemoryError')) ?? false,
        deployedMemory: liveDefinition?.memory,
        draftMemory: definitionDraft.memory,
        draftCpu: definitionDraft.cpu,
      })
    : undefined

  const sections: ChangeSections = {
    target: !definitionChanged || !definitionDraft ? null : editedDefinition ? { kind: 'definition', definition: editedDefinition } : preloaded ? { kind: 'revision', revision: revisionParam as number } : suggestedDefinition ? { kind: 'definition', definition: definitionDraft } : null,
    service: settings && serviceDraft ? serviceDraftToChange(settings, serviceDraft) : null,
    scaling: settings && scalingDraft ? scalingDraftToChange(settings, scalingDraft) : null,
  }
  const changes = countSections(sections)
  const problems = [
    ...definitionProblems,
    ...(settings && serviceDraft ? serviceDraftErrors(settings, serviceDraft) : []),
    ...(settings && scalingDraft ? scalingDraftErrors(settings, scalingDraft) : []),
  ]
  const predictedRevision = Math.max(deployedRevision ?? 0, ...(revisions.data?.items.map((item) => item.revision) ?? [])) + 1
  const baseline: EditBase = { revision: deployedRevision ?? 0, version: settings?.version ?? '' }
  const rows = pendingRows({ sections, liveDefinition, definitionAfter: definitionChanged ? definitionDraft : undefined }, settings)
  const canDeploy = settings?.can.deploy ?? service.data?.can.deploy ?? false
  const base = editBase ?? baseline

  // Remember what the first edit was based on, so a change made by someone else in the meantime is caught on submit.
  const edit =
    <T,>(setter: (value: T) => void) =>
    (value: T) => {
      setEditBase((current) => current ?? baseline)
      setter(value)
    }

  const clearPresetParams = () => {
    const presets = ['revision', ...RIGHTSIZE_PARAMS]
    if (presets.some((name) => searchParams.has(name))) setSearchParams((previous) => { presets.forEach((name) => previous.delete(name)); return previous }, { replace: true })
  }

  const resetDrafts = () => {
    setEditedService(null)
    setEditedScaling(null)
    setEditedDefinition(null)
    setEditBase(null)
    setPartial(null)
    setReason(null)
    setReviewing(false)
    setPendingSheetOpen(false)
    setSubmitError(undefined)
    setMovedFrom(null)
    setSettingsChanged(false)
    clearPresetParams()
  }

  // Takes fresh settings as the new baseline; the user's edits stay where they still differ from what is live now.
  const adoptSettings = (fresh: DeploySettingsResponse) => {
    queryClient.setQueryData(['service', serviceId, 'deploy-settings'], fresh)
    if (settings && editedService) setEditedService(rebaseServiceDraft(editedService, settings, fresh))
    if (settings && editedScaling) setEditedScaling(rebaseScalingDraft(editedScaling, settings))
    setEditBase((current) => ({ revision: current?.revision ?? baseline.revision, version: fresh.version }))
  }

  const onFinished = (response: ChangeSetResponse) => {
    if (response.status === 'registered_only') {
      const registered = response.registered_revision === null ? '' : ` :${response.registered_revision}`
      toast.show(`Registered${registered}. Someone with Deploy on ${tagName} can roll it out.`, 'ok')
    } else {
      toast.show(response.to_revision === null ? `Applied the changes to ${serviceName}.` : `Deploying ${serviceName} to :${response.to_revision}.`, 'ok')
      if (response.to_revision !== null) {
        const revision = response.to_revision
        setStartedRollout(revision)
        // Gives up after a minute so a rollout that never started does not leave the note on screen for good.
        setTimeout(() => setStartedRollout((current) => (current === revision ? null : current)), ROLLOUT_NOTE_MAX_MS)
      }
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

  const apply = () => void submit(buildChangeSetRequest({ baseRevision: base.revision, baseVersion: base.version, reason: shownReason, sections }))
  const retry = () => {
    const next = partial && buildRetryRequest(partial.request, partial.response)
    if (next) void submit(next)
  }
  // Without the deployed revision there is nothing to guard the change set with, so Review waits for it.
  const reviewable = (changes > 0 || partial !== null) && problems.length === 0 && deployedRevision !== undefined

  const asideProps = {
    count: changes,
    rows,
    problems,
    loadedRevision: revisionPending
      ? {
          text: revisionTarget.isError ? `Revision :${revisionParam} could not be loaded` : revisionTarget.data ? `:${revisionParam} is loaded from the revisions list` : `Loading :${revisionParam}…`,
          dismissLabel: `Dismiss :${revisionParam}`,
          onDismiss: clearPresetParams,
        }
      : suggested && suggestedDefinition && liveDefinition
        ? {
            text: isDefinitionChanged(suggestedDefinition, liveDefinition) ? `Task size ${suggested.cpu} / ${suggested.memory} is suggested from Cost` : 'This service already has this size.',
            dismissLabel: 'Dismiss suggestion',
            onDismiss: clearPresetParams,
          }
        : undefined,
    discardDisabled: changes === 0 && partial === null,
    reviewDisabled: !reviewable,
    onDiscard: resetDrafts,
    onReview: () => {
      setEditBase((current) => current ?? baseline)
      setReviewing(true)
    },
  }

  // ?review=1 (from the timeline's rollback) is consumed once the revision is in the draft: Review opens then if there is something to
  // review, and never while a rollout runs. The user still applies.
  const reviewRequested = searchParams.get('review') === '1'
  const rolloutStateKnown = service.data !== undefined && (revisions.data !== undefined || revisions.isError)
  const rolloutRunning = service.data !== undefined && isRolloutRunning(service.data, revisions.data)
  const revisionIsDeployed = revisionParam !== null && revisionParam === deployedRevision
  const revisionReady = revisionTarget.data !== undefined && liveDefinition !== undefined && deployedRevision !== undefined
  useEffect(() => {
    if (!reviewRequested || !rolloutStateKnown) return
    const nothingToReview = revisionParam === null || revisionTarget.isError || taskDefinition.isError || revisionIsDeployed
    if (!rolloutRunning && !nothingToReview && !revisionReady) return
    if (!rolloutRunning && !nothingToReview && reviewable) asideProps.onReview()
    setSearchParams(
      (previous) => {
        const next = new URLSearchParams(previous)
        next.delete('review')
        return next
      },
      { replace: true },
    )
    // eslint-disable-next-line react-hooks/exhaustive-deps -- onReview only sets state from values already listed here
  }, [reviewRequested, rolloutStateKnown, rolloutRunning, revisionReady, reviewable, revisionParam, revisionTarget.isError, taskDefinition.isError, revisionIsDeployed])

  const showingProgress = service.data !== undefined && shouldShowProgress(service.data, now)
  const awaitingRollout = startedRollout !== null && !showingProgress && (deployedRevision ?? 0) < startedRollout

  return (
    <>
      {revisionIsDeployed && (
        <Banner kind="info">Revision :{revisionParam} is already deployed.</Banner>
      )}
      {awaitingRollout && (
        <div role="status" aria-label="Rollout starting">
          <Banner kind="info">Rolling out :{startedRollout}. The fields below show the new image once ECS reports it.</Banner>
        </div>
      )}
      {service.data && shouldShowProgress(service.data, now) && (
        <RolloutProgress serviceId={serviceId} service={service.data} canDeploy={service.data.can.deploy} tagName={tagName} />
      )}
      <div className="hm-split-sticky">
        <div className={`hm-wide-form ${styles.form}`} data-changes={changes} data-definition-changed={definitionChanged} data-definition-problems={definitionProblems.length}>
          {definitionDraft && liveDefinition ? (
            <>
              <ContainersPanel
                serviceId={serviceId}
                definition={definitionDraft}
                deployed={liveDefinition}
                onChange={edit(setEditedDefinition)}
                canEdit={canEditDefinition}
                isAdmin={settings?.can.admin ?? false}
                tagName={tagName}
              />
              <TaskSizePanel definition={definitionDraft} onChange={edit(setEditedDefinition)} canEdit={canEditDefinition} tagName={tagName} oomHint={oomHint} />
            </>
          ) : (
            <>
              {(['Containers', 'Task size'] as const).map((title) => (
                <Panel
                  key={title}
                  title={title}
                  loading={!taskDefinition.isError}
                  error={taskDefinition.isError ? { message: 'The task definition could not be loaded.', onRetry: () => void taskDefinition.refetch() } : undefined}
                >
                  {null}
                </Panel>
              ))}
            </>
          )}
          {settings && serviceDraft && scalingDraft ? (
            <>
              <ServicePanel settings={settings} draft={serviceDraft} onChange={edit(setEditedService)} tagName={tagName} />
              <ScalingPanel
                settings={settings}
                draft={scalingDraft}
                onChange={edit(setEditedScaling)}
                desiredCount={serviceDraft.desiredCount}
                tagName={tagName}
                onRetry={() => void settingsQuery.refetch()}
              />
            </>
          ) : (
            <>
              {(['Service', 'Auto scaling'] as const).map((title) => (
                <Panel key={title} title={title} loading={settingsError === undefined} error={settingsError}>
                  {null}
                </Panel>
              ))}
            </>
          )}
        </div>

        {!phone && <PendingChangesAside {...asideProps} />}
      </div>

      {phone && (changes > 0 || partial !== null || problems.length > 0 || asideProps.loadedRevision !== undefined) && (
        <PendingChangesBar
          {...asideProps}
          sheetOpen={pendingSheetOpen}
          onSheetOpenChange={setPendingSheetOpen}
        />
      )}

      {reviewing && (
        <ReviewChangesDialog
          serviceName={serviceName}
          tagName={tagName}
          deployedRevision={deployedRevision ?? 0}
          predictedRevision={predictedRevision}
          sections={sections}
          definitionBefore={liveDefinition ? pretty(liveDefinition) : ''}
          definitionAfter={definitionDraft ? pretty(definitionDraft) : ''}
          serviceRows={settings && sections.service ? serviceRows(settings, sections.service) : []}
          scalingLines={settings && sections.scaling ? scalingLines(settings, sections.scaling) : []}
          canDeploy={canDeploy}
          reason={shownReason}
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
