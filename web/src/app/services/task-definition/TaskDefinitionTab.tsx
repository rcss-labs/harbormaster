import { useState } from 'react'
import { useNavigate } from 'react-router'
import { isApiError, useRegisterRevision, useRevisions, useTaskDefinition, type RegisterRevisionBody } from '../../../api/change'
import type { ServiceDTO } from '../../../api/types'
import { Banner } from '../../../components/Banner'
import { TagChip } from '../../../components/TagChip'
import { showWarning, useToast } from '../../../components/Toast'
import { scanGateOf, useGatedSubmit } from '../deploy/useGatedSubmit'
import { queryPanelError } from '../panelError'
import { isRolloutRunning } from '../rollout'
import { JsonEditor } from './JsonEditor'
import { Panel } from '../../../components/Panel'
import { ReviewDialog } from './ReviewDialog'
import { RevisionsTable } from './RevisionsTable'
import { type Definition } from './mainContainer'
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
  const register = useRegisterRevision(serviceId)
  const toast = useToast()
  const navigate = useNavigate()

  // null = follow the deployed definition; set on the first edit and cleared on discard or after registering.
  const [draft, setDraft] = useState<Draft | null>(null)
  const [reviewing, setReviewing] = useState(false)
  const [registerError, setRegisterError] = useState<string | undefined>()
  const [movedFrom, setMovedFrom] = useState<number | null>(null)
  const closeReview = () => {
    setReviewing(false)
    setRegisterError(undefined)
    setMovedFrom(null)
  }

  // The scan gate is not a failure: it propagates to the gate hook, which shows it in the review dialog.
  const sendRegistration = async (body: RegisterRevisionBody) => {
    setRegisterError(undefined)
    try {
      const result = await register.mutateAsync(body)
      closeReview()
      setDraft(null)
      showWarning(toast, result.warning)
      toast.show(`${service.name}:${result.revision} is registered. Deploy it from the Deploy tab when you are ready.`, 'ok')
    } catch (failure) {
      if (scanGateOf(failure)) throw failure
      if (isApiError(failure, 'conflict') && draft) {
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
  const gated = useGatedSubmit(sendRegistration)
  const keepEditing = () => {
    gated.cancel()
    closeReview()
  }
  const data = taskDefinition.data
  if (!data) {
    return (
      <Panel
        title="Task definition JSON"
        loading={!taskDefinition.isError}
        error={queryPanelError(taskDefinition, 'The task definition could not be read.')}
      >
        {null}
      </Panel>
    )
  }

  const canEdit = service.can.edit
  const initialText = pretty(data.definition)
  const text = draft?.text ?? initialText
  const dirty = trimEnd(text) !== initialText
  const { definition, error } = parseDefinition(text)

  const setText = (next: string) =>
    setDraft(trimEnd(next) === initialText ? null : { text: next, baseRevision: draft?.baseRevision ?? data.deployed_revision })
  const predictedRevision = Math.max(data.deployed_revision, ...(revisions.data?.items.map((item) => item.revision) ?? [])) + 1

  const onRegister = () => {
    if (!draft || !definition) return
    void gated.submit({ base_revision: draft.baseRevision, definition })
  }

  const openInDeployTab = (revision: number) => navigate(`/services/${serviceId}/deploy?revision=${revision}`)
  const rolloutRunning = isRolloutRunning(service, revisions.data)
  const lockReason = service.can.deploy ? undefined : `You need Deploy on ${service.tag.name}`
  const controllerReason = service.controller.supported ? undefined : service.controller.banner

  return (
    <>
      {canEdit ? (
        <Banner kind="info">
          <>
            Saving registers a new revision of <code>{service.name}</code>. Deploy it afterwards from the Deploy tab. Terraform will show the new revision as
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
        <Panel title="Task definition JSON" actions={<span className={styles.note}>{`based on :${draft?.baseRevision ?? data.deployed_revision}`}</span>}>
          <JsonEditor
            text={text}
            initialText={initialText}
            registerRevision={predictedRevision}
            error={error}
            dirty={dirty}
            readOnly={!canEdit}
            onChange={setText}
            onDiscard={() => setDraft(null)}
            onReview={() => setReviewing(true)}
          />
        </Panel>
      </div>
      {revisions.data && (
        <RevisionsTable
          serviceName={service.name}
          revisions={revisions.data}
          canDeploy={service.can.deploy}
          deployDisabledReason={lockReason ?? controllerReason}
          rolloutRunning={rolloutRunning}
          onDeploy={openInDeployTab}
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
          registering={register.isPending || gated.busy}
          error={registerError}
          onKeepEditing={keepEditing}
          onRegister={onRegister}
          scanGate={
            gated.gate
              ? { serviceId, gate: gated.gate, onRowSettled: gated.settleRow, onDeployAnyway: gated.deployAnyway, onCancel: gated.cancel }
              : undefined
          }
        />
      )}
    </>
  )
}
