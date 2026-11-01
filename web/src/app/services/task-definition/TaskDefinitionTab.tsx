import { useEffect, useState } from 'react'
import { useNavigate, useSearchParams } from 'react-router'
import { isApiError, useDeploy, useImageTags, useRegisterRevision, useRevisions, useTaskDefinition, type RegisterResult } from '../../../api/change'
import { useServiceTasks } from '../../../api/hooks'
import type { RevisionsResponse, ServiceDTO } from '../../../api/types'
import { Banner } from '../../../components/Banner'
import { EmptyState } from '../../../components/EmptyState'
import { TagChip } from '../../../components/TagChip'
import { showWarning, useToast } from '../../../components/Toast'
import { isRolloutRunning } from '../rollout'
import { DeployDialog } from './DeployDialog'
import { JsonEditor } from './JsonEditor'
import { QuickEdit, isTagInvalid } from './QuickEdit'
import { ReviewDialog } from './ReviewDialog'
import { RevisionsTable } from './RevisionsTable'
import { mainImageTag, nextFargateMemory, type Definition } from './mainContainer'
import styles from './taskDefinition.module.css'

interface Draft {
  text: string
  baseRevision: number
}

/** Ignore trailing whitespace so a final newline neither marks the draft dirty nor adds a diff line. */
const trimEnd = (text: string) => text.replace(/\s+$/, '')
const pretty = (definition: unknown) => JSON.stringify(definition, null, 2)

function parseDefinition(text: string): { definition: Definition | null; error: string | null } {
  try {
    const parsed: unknown = JSON.parse(text)
    if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed)) {
      return { definition: null, error: 'The task definition must be a JSON object.' }
    }
    return { definition: parsed as Definition, error: null }
  } catch (error) {
    return { definition: null, error: error instanceof Error ? error.message : 'Invalid JSON.' }
  }
}

type PickedTarget = { revision: number; mode: 'deploy' | 'rollback' }

/** Turns ?deploy=N into a dialog target, but only when the same gates as the table's buttons allow it. */
function requestedTarget(param: string | null, service: ServiceDTO, revisions: RevisionsResponse | undefined): PickedTarget | null {
  if (param === null || !revisions) return null
  if (!service.can.deploy || !service.controller.supported || isRolloutRunning(service, revisions)) return null
  const item = revisions.items.find((candidate) => String(candidate.revision) === param)
  if (item?.status === 'not_deployed') return { revision: item.revision, mode: 'deploy' }
  if (item?.status === 'previous') return { revision: item.revision, mode: 'rollback' }
  return null
}

export function TaskDefinitionTab({ serviceId, service }: { serviceId: string; service: ServiceDTO }) {
  const taskDefinition = useTaskDefinition(serviceId)
  const revisions = useRevisions(serviceId)
  const imageTags = useImageTags(serviceId, true)
  const tasks = useServiceTasks(serviceId)
  const register = useRegisterRevision(serviceId)
  const deploy = useDeploy(serviceId)
  const toast = useToast()
  const navigate = useNavigate()
  const [searchParams, setSearchParams] = useSearchParams()

  // null = follow the deployed definition; set on the first edit and cleared on discard or after registering.
  const [draft, setDraft] = useState<Draft | null>(null)
  const [reviewing, setReviewing] = useState(false)
  const [registerError, setRegisterError] = useState<string | undefined>()
  const [movedFrom, setMovedFrom] = useState<number | null>(null)
  const [registered, setRegistered] = useState<RegisterResult | null>(null)
  // A revision picked from the table; the pending-revision banner can also request one with ?deploy=N.
  const [picked, setPicked] = useState<PickedTarget | null>(null)
  const deployParam = searchParams.get('deploy')
  const requested = requestedTarget(deployParam, service, revisions.data)

  // Drop a ?deploy=N that cannot open a dialog, once the revisions have loaded to judge it.
  useEffect(() => {
    if (deployParam !== null && revisions.data && !requested) {
      setSearchParams((previous) => { previous.delete('deploy'); return previous }, { replace: true })
    }
  }, [deployParam, revisions.data, requested, setSearchParams])

  const data = taskDefinition.data
  if (taskDefinition.isError && !data) {
    return <EmptyState>{taskDefinition.error instanceof Error ? taskDefinition.error.message : 'The task definition could not be loaded.'}</EmptyState>
  }
  if (!data) return null

  const canEdit = service.can.edit
  const initialText = pretty(data.definition)
  const text = draft?.text ?? initialText
  const dirty = trimEnd(text) !== initialText
  const { definition, error } = parseDefinition(text)

  const deployedTag = mainImageTag(data.definition, service.name)
  const draftTag = definition ? mainImageTag(definition, service.name) : ''
  const tagInvalid = definition !== null && isTagInvalid(imageTags.data?.source, draftTag, deployedTag)

  const setText = (next: string) =>
    setDraft(trimEnd(next) === initialText ? null : { text: next, baseRevision: draft?.baseRevision ?? data.deployed_revision })
  const predictedRevision = Math.max(data.deployed_revision, ...(revisions.data?.items.map((item) => item.revision) ?? [])) + 1

  const oomHint = (() => {
    const deployedMemory = Number(data.definition.memory)
    const draftMemory = Number(definition?.memory ?? deployedMemory)
    const hit = tasks.data?.items.some((task) => task.status === 'STOPPED' && task.stopped_reason.includes('OutOfMemoryError'))
    if (!hit || !Number.isFinite(deployedMemory) || draftMemory > deployedMemory) return undefined
    const suggested = nextFargateMemory(String(definition?.cpu ?? data.definition.cpu), deployedMemory * 2)
    return suggested && suggested > deployedMemory ? { memory: String(deployedMemory), suggested: String(suggested) } : undefined
  })()

  const closeReview = () => {
    setReviewing(false)
    setRegisterError(undefined)
    setMovedFrom(null)
  }

  const onRegister = async () => {
    if (!draft || !definition) return
    setRegisterError(undefined)
    try {
      const result = await register.mutateAsync({ base_revision: draft.baseRevision, definition })
      closeReview()
      setDraft(null)
      showWarning(toast, result.warning)
      if (result.can_deploy) setRegistered(result)
      else toast.show(`Registered. Someone with Deploy on ${result.tag.name} can roll it out.`, 'ok')
    } catch (failure) {
      if (isApiError(failure, 'conflict')) {
        // The service moved on: take the new deployed revision from a fresh read and show the diff again.
        const fresh = await taskDefinition.refetch()
        if (fresh.isError || !fresh.data) {
          setRegisterError(fresh.error instanceof Error ? fresh.error.message : 'The service could not be re-read after the conflict.')
        } else {
          setMovedFrom(draft.baseRevision)
          setDraft({ ...draft, baseRevision: fresh.data.deployed_revision })
        }
      } else {
        setRegisterError(failure instanceof Error ? failure.message : 'The revision could not be registered.')
      }
    }
  }

  const runDeploy = async (revision: number, onStarted: () => void) => {
    try {
      const started = await deploy.mutateAsync({ revision })
      showWarning(toast, started.warning)
      onStarted()
      navigate(`/services/${serviceId}/overview`)
    } catch (failure) {
      if (isApiError(failure, 'conflict')) toast.show('A rollout is already running', 'bad')
      else toast.show(failure instanceof Error ? failure.message : 'The deploy could not be started.', 'bad')
    }
  }

  const onDeploy = () => {
    if (registered) void runDeploy(registered.revision, () => setRegistered(null))
  }

  const target = picked ?? requested
  const targetRevision = target ? revisions.data?.items.find((item) => item.revision === target.revision) : undefined
  const closePicked = () => {
    setPicked(null)
    if (searchParams.has('deploy')) setSearchParams((previous) => { previous.delete('deploy'); return previous }, { replace: true })
  }
  const rolloutRunning = isRolloutRunning(service, revisions.data)
  const lockReason = service.can.deploy ? undefined : `You need Deploy on ${service.tag.name}`
  const controllerReason = service.controller.supported ? undefined : service.controller.banner

  return (
    <>
      {canEdit ? (
        <Banner kind="info">
          <>
            Saving registers a new revision of <code>{service.name}</code>. Harbormaster then asks whether to deploy it. Terraform will show the new revision as
            drift on its next plan. To keep Terraform from reverting a deploy, add <code>lifecycle {'{'} ignore_changes = [task_definition, desired_count] {'}'}</code> to
            the service.
          </>
        </Banner>
      ) : (
        <Banner kind="warn">
          <>
            Read only. You need <strong>Edit task definition</strong> on <TagChip tag={service.tag} /> to register a new revision.
          </>
        </Banner>
      )}
      <div className={styles.layout}>
        <div>
          <div className={styles.cardHead}>
            <h2>Quick edit</h2>
            <span className={styles.note}>Edits the JSON</span>
          </div>
          {definition ? (
            <QuickEdit
              definition={definition}
              mainContainer={data.main_container}
              imageTags={imageTags.data}
              deployedTag={deployedTag}
              oomHint={oomHint}
              disabled={!canEdit}
              onChange={(next) => setText(pretty(next))}
            />
          ) : (
            <Banner kind="bad">{`The JSON has an error, so the form is paused: ${error}`}</Banner>
          )}
        </div>
        <div>
          <div className={styles.cardHead}>
            <h2>Task definition JSON</h2>
            <span className={styles.note}>{`based on :${draft?.baseRevision ?? data.deployed_revision}`}</span>
          </div>
          <JsonEditor
            text={text}
            error={error}
            dirty={dirty}
            readOnly={!canEdit}
            blocked={tagInvalid}
            onChange={setText}
            onDiscard={() => setDraft(null)}
            onReview={() => setReviewing(true)}
          />
        </div>
      </div>
      {revisions.data && (
        <RevisionsTable
          serviceName={service.name}
          revisions={revisions.data}
          canDeploy={service.can.deploy}
          deployDisabledReason={lockReason ?? controllerReason}
          rolloutRunning={rolloutRunning}
          onDeploy={(revision, mode) => setPicked({ revision, mode })}
        />
      )}
      {reviewing && draft && (
        <ReviewDialog
          serviceName={service.name}
          deployedRevision={data.deployed_revision}
          predictedRevision={predictedRevision}
          baseRevision={movedFrom ?? draft.baseRevision}
          before={initialText}
          after={trimEnd(text)}
          registering={register.isPending}
          error={registerError}
          onKeepEditing={closeReview}
          onRegister={onRegister}
        />
      )}
      {registered && (
        <DeployDialog
          mode="prompt"
          serviceName={service.name}
          clusterName={service.cluster.name}
          revision={registered.revision}
          deployedRevision={data.deployed_revision}
          runningCount={service.running}
          imageTag={registered.image_tag}
          cpu={registered.cpu}
          memory={registered.memory}
          strategy={registered.strategy}
          effectiveTag={registered.tag.name}
          tagInherited={registered.tag.inherited}
          tagColor={registered.tag.color}
          busy={deploy.isPending}
          onDismiss={() => {
            toast.show(`${service.name}:${registered.revision} is registered. Deploy it from the service page when you are ready.`, 'info')
            setRegistered(null)
          }}
          onConfirm={onDeploy}
        />
      )}
      {target && targetRevision && !registered && (
        <DeployDialog
          mode={target.mode}
          serviceName={service.name}
          clusterName={service.cluster.name}
          revision={targetRevision.revision}
          deployedRevision={data.deployed_revision}
          runningCount={service.running}
          imageTag={targetRevision.image_tag}
          cpu={targetRevision.cpu}
          memory={targetRevision.memory}
          strategy={service.strategy_text}
          effectiveTag={service.tag.name}
          tagInherited={service.tag.inherited}
          tagColor={service.tag.color}
          busy={deploy.isPending}
          onDismiss={closePicked}
          onConfirm={() => void runDeploy(targetRevision.revision, closePicked)}
        />
      )}
    </>
  )
}
