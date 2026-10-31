import type { TagColor } from '../../../api/types'
import { Banner } from '../../../components/Banner'
import { Dialog } from '../../../components/Dialog'
import { KV } from '../../../components/KV'
import { TagChip } from '../../../components/TagChip'

interface DeployDialogProps {
  mode: 'prompt' | 'deploy' | 'rollback'
  serviceName: string
  clusterName: string
  revision: number
  deployedRevision: number
  runningCount: number
  imageTag: string
  cpu: string
  memory: string
  strategy: string
  effectiveTag: string
  tagInherited: boolean
  tagColor?: TagColor
  busy: boolean
  onDismiss(): void
  onConfirm(): void
}

export function DeployDialog(props: DeployDialogProps) {
  const { mode, serviceName, revision } = props
  const title = {
    prompt: `Revision :${revision} is registered. Deploy it now?`,
    deploy: `Deploy ${serviceName} to :${revision}?`,
    rollback: `Roll back ${serviceName} to :${revision}?`,
  }[mode]
  const taskWord = props.runningCount === 1 ? 'task' : 'tasks'
  const tag = { id: 0, name: props.effectiveTag, color: props.tagColor ?? 'neutral', inherited: props.tagInherited }

  return (
    <Dialog
      title={title}
      sub={`${serviceName} runs :${props.deployedRevision} on ${props.runningCount} ${taskWord} in ${props.clusterName}.`}
      onClose={props.onDismiss}
      actions={[
        { label: mode === 'prompt' ? 'Not now' : 'Cancel', onClick: props.onDismiss },
        {
          label: mode === 'rollback' ? `Roll back to :${revision}` : `Deploy :${revision}`,
          kind: 'primary',
          onClick: props.onConfirm,
          disabled: props.busy,
        },
      ]}
    >
      <KV
        rows={[
          ['Image', <span key="image" className="mono">{`${serviceName}:${props.imageTag}`}</span>],
          ['Size', `${props.cpu} CPU · ${props.memory} MiB`],
          ['Strategy', props.strategy],
          ['Tag', <TagChip key="tag" tag={tag} />],
        ]}
      />
      {props.effectiveTag === 'Prod' && <Banner kind="warn">This is a Prod service.</Banner>}
    </Dialog>
  )
}
