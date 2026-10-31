import type { TagDTO } from '../api/types'
import { Banner } from './Banner'
import { TagChip } from './TagChip'

export type LockAction = 'View' | 'Logs' | 'Operate' | 'Deploy' | 'Edit task definition' | 'Terminal'

export function LockPanel({ action, tag }: { action: LockAction; tag: TagDTO }) {
  return (
    <Banner kind="warn">
      You need <strong>{action}</strong> on <TagChip tag={tag} /> to use this. Ask a global admin to change your
      permissions.
    </Banner>
  )
}
