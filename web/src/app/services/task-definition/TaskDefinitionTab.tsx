import { useState } from 'react'
import { useNavigate } from 'react-router'
import { isApiError, useDeploy, useImageTags, useRegisterRevision, useRevisions, useTaskDefinition, type RegisterResult } from '../../../api/change'
import { useServiceTasks } from '../../../api/hooks'
import type { ServiceDTO } from '../../../api/types'
import { Banner } from '../../../components/Banner'
import { EmptyState } from '../../../components/EmptyState'
import { TagChip } from '../../../components/TagChip'
import { useToast } from '../../../components/Toast'
import { DeployDialog } from './DeployDialog'
import { JsonEditor } from './JsonEditor'
import { QuickEdit, isTagInvalid } from './QuickEdit'
import { ReviewDialog } from './ReviewDialog'
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

export function TaskDefinitionTab({ serviceId, service }: { serviceId: string; service: ServiceDTO }) {
  const taskDefinition = useTaskDefinition(serviceId)
  const revisions = useRevisions(serviceId)
  const imageTags = useImageTags(serviceId, true)
  const tasks = useServiceTasks(serviceId)
  const register = useRegisterRevision(serviceId)
  const deploy = useDeploy(serviceId)
  const toast = useToast()
  const navigate = useNavigate()

  // null = follow the deployed definition; set on the first edit and cleared on discard or after registering.
  const [draft, setDraft] = useState<Draft | null>(null)
  const [reviewing, setReviewing] = useState(false)
  const [registerError, setRegisterError] = useState<string | undefined>()
  const [movedFrom, setMovedFrom] = useState<number | null>(null)
  const [registered, setRegistered] = useState<RegisterResult | null>(null)

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

  const onDeploy = async () => {
    if (!registered) return
    try {
      await deploy.mutateAsync({ revision: registered.revision })
      setRegistered(null)
      navigate(`/services/${serviceId}/overview`)
    } catch (failure) {
      if (isApiError(failure, 'conflict')) toast.show('A rollout is already running', 'bad')
      else toast.show(failure instanceof Error ? failure.message : 'The deploy could not be started.', 'bad')
    }
  }

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
      {/* revisions */}
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
    </>
  )
}
