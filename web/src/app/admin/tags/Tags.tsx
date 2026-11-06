import { useState } from 'react'
import { useTagAssignments, useTags } from '../../../api/hooks'
import type { TagItemDTO } from '../../../api/types'
import { BottomSheet, SheetAction } from '../../../components/BottomSheet'
import { DataTable, type DataTableColumn } from '../../../components/DataTable'
import { Icon } from '../../../components/Icon'
import { Panel } from '../../../components/Panel'
import { Table, tableClass } from '../../../components/Table'
import { usePhone } from '../../../components/useNarrow'
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

const DEFAULT_TAG_REASON = 'Dev is the default tag'
const KEPT_IN_HARBORMASTER = "Tags live only in Harbormaster; AWS tags are never read or written. An untagged cluster is Dev; an untagged service inherits its cluster's tag."

function usersWithAccess(tag: TagItemDTO) {
  return tag.users.length > 0 ? (
    <div className={styles.chips}>
      {tag.users.map((user) => (
        <span key={user.username} className={styles.chip}>
          {user.username} <span className={styles.muted}>{user.preset}</span>
        </span>
      ))}
    </div>
  ) : (
    <span className={styles.muted}>Global admins only</span>
  )
}

function PhoneTagsTable({ tags, onRename, onDelete }: TagsTableProps) {
  const [menuFor, setMenuFor] = useState<number | null>(null)
  const menuTag = tags.find((tag) => tag.id === menuFor)
  const columns: DataTableColumn<TagItemDTO>[] = [
    {
      key: 'tag',
      header: 'Tag',
      phone: 'title',
      render: (tag) => (
        <span className={styles.tagTitle}>
          <TagChip tag={{ id: tag.id, name: tag.name, color: tag.color, inherited: false }} />
          {tag.builtin && <span className={`${styles.muted} ${styles.small}`}>default</span>}
        </span>
      ),
    },
    { key: 'clusters', header: 'Clusters', render: (tag) => `${tag.clusters} clusters` },
    { key: 'direct', header: 'Services tagged directly', render: (tag) => `${tag.services_direct} services tagged directly` },
    { key: 'effective', header: 'Services in effect', render: (tag) => `${tag.services_effective} services in effect` },
    { key: 'users', header: 'Users with access', render: usersWithAccess },
  ]
  return (
    <Panel title="Tags" flush actions={<span title={KEPT_IN_HARBORMASTER}>Kept in Harbormaster only</span>}>
      <DataTable
        label="Tags"
        columns={columns}
        rows={tags}
        rowKey={(tag) => String(tag.id)}
        rowAction={(tag) => (
          <button type="button" className={styles.moreBtn} aria-label={`Actions for tag ${tag.name}`} aria-haspopup="menu" onClick={() => setMenuFor(tag.id)}>
            <Icon name="more" size={18} />
          </button>
        )}
      />
      {menuTag && (
        <BottomSheet open onClose={() => setMenuFor(null)} label={`Actions for tag ${menuTag.name}`} title={menuTag.name} role="menu">
          <SheetAction
            label="Rename"
            disabled={menuTag.builtin}
            why={menuTag.builtin ? DEFAULT_TAG_REASON : undefined}
            onClick={() => {
              setMenuFor(null)
              onRename(menuTag)
            }}
          />
          <SheetAction
            label="Delete"
            danger
            disabled={menuTag.builtin}
            why={menuTag.builtin ? DEFAULT_TAG_REASON : undefined}
            onClick={() => {
              setMenuFor(null)
              onDelete(menuTag)
            }}
          />
        </BottomSheet>
      )}
    </Panel>
  )
}

function TagsTable(props: TagsTableProps) {
  const phone = usePhone()
  return phone ? <PhoneTagsTable {...props} /> : <DesktopTagsTable {...props} />
}

function DesktopTagsTable({ tags, onRename, onDelete }: TagsTableProps) {
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
