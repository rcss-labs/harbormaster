import { useState } from 'react'
import { useTagAssignments, useTags } from '../../../api/hooks'
import type { TagItemDTO } from '../../../api/types'
import { Panel } from '../../../components/Panel'
import { Table, tableClass } from '../../../components/Table'
import { TagChip } from '../../../components/TagChip'
import { useAuth } from '../../auth/AuthProvider'
import { HeaderAction } from '../../settings/HeaderAction'
import { useFilters } from '../../shell/useFilters'
import { Assignments } from './Assignments'
import { DeleteTagDialog } from './DeleteTagDialog'
import { TagDialog } from './TagDialog'
import styles from './tags.module.css'

type DialogState = { kind: 'create' } | { kind: 'rename' | 'delete'; tag: TagItemDTO } | null

export function Tags() {
  const { me } = useAuth()
  const isAdmin = me?.admin ?? false
  // Non-admins would get 403s, so the admin queries stay disabled for them.
  const tags = useTags({ enabled: isAdmin })
  const { filters } = useFilters()
  // Only the assignments follow the account and region filters; the tag list stays global.
  const assignments = useTagAssignments(filters, { enabled: isAdmin })
  const [dialog, setDialog] = useState<DialogState>(null)

  if (!me || !isAdmin) return null

  const close = () => setDialog(null)

  return (
    <>
      <HeaderAction>
        <button type="button" className={`${styles.btn} ${styles.primary}`} onClick={() => setDialog({ kind: 'create' })}>
          Create tag
        </button>
      </HeaderAction>
      <div className={styles.stack}>
        {tags.data && (
          <TagsTable
            tags={tags.data.items}
            onRename={(tag) => setDialog({ kind: 'rename', tag })}
            onDelete={(tag) => setDialog({ kind: 'delete', tag })}
          />
        )}
        {tags.data && assignments.data && <Assignments clusters={assignments.data.clusters} tags={tags.data.items} />}
      </div>
      {dialog?.kind === 'create' && <TagDialog onClose={close} />}
      {dialog?.kind === 'rename' && <TagDialog existing={dialog.tag} onClose={close} />}
      {dialog?.kind === 'delete' && <DeleteTagDialog tag={dialog.tag} onClose={close} />}
    </>
  )
}

interface TagsTableProps {
  tags: TagItemDTO[]
  onRename: (tag: TagItemDTO) => void
  onDelete: (tag: TagItemDTO) => void
}

function TagsTable({ tags, onRename, onDelete }: TagsTableProps) {
  return (
    <Panel
      title="Tags"
      flush
      actions={
        <span title="Tags live only in Harbormaster; AWS tags are never read or written. An untagged cluster is Dev; an untagged service inherits its cluster's tag.">
          Kept in Harbormaster only
        </span>
      }
    >
      <Table>
        <thead>
          <tr>
            <th>Tag</th>
            <th className={tableClass.num}>Clusters</th>
            <th className={tableClass.num}>Services tagged directly</th>
            <th className={tableClass.num}>Services in effect</th>
            <th>Users with access</th>
            <th />
          </tr>
        </thead>
        <tbody>
          {tags.map((tag) => (
            <tr key={tag.id}>
              <td>
                <TagChip tag={{ id: tag.id, name: tag.name, color: tag.color, inherited: false }} />
                {tag.builtin && (
                  <>
                    {' '}
                    <span className={`${styles.muted} ${styles.small}`}>default</span>
                  </>
                )}
              </td>
              <td className={tableClass.num}>{tag.clusters}</td>
              <td className={tableClass.num}>{tag.services_direct}</td>
              <td className={tableClass.num}>{tag.services_effective}</td>
              <td>
                {tag.users.length > 0 ? (
                  <div className={styles.chips}>
                    {tag.users.map((user) => (
                      <span key={user.username} className={styles.chip}>
                        {user.username} <span className={styles.muted}>{user.preset}</span>
                      </span>
                    ))}
                  </div>
                ) : (
                  <span className={styles.muted}>Global admins only</span>
                )}
              </td>
              <td className={tableClass.num}>
                <button
                  type="button"
                  className={`${styles.btn} ${styles.sm}`}
                  disabled={tag.builtin}
                  title={tag.builtin ? 'Dev is the default tag' : undefined}
                  onClick={() => onRename(tag)}
                >
                  Rename
                </button>{' '}
                <button
                  type="button"
                  className={`${styles.btn} ${styles.sm} ${styles.danger}`}
                  disabled={tag.builtin}
                  title={tag.builtin ? 'Dev is the default tag' : undefined}
                  onClick={() => onDelete(tag)}
                >
                  Delete
                </button>
              </td>
            </tr>
          ))}
        </tbody>
      </Table>
    </Panel>
  )
}
