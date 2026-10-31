import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { Link } from 'react-router'
import { useTags } from '../../../api/hooks'
import type { TagDTO } from '../../../api/types'
import { Dialog } from '../../../components/Dialog'
import { TagChip } from '../../../components/TagChip'
import { useToast } from '../../../components/Toast'
import styles from './tags.module.css'
import { type AssignmentKind, assignedTagId, chosenTag, invalidateTagQueries, saveAssignment } from './tagApi'

interface EditTagDialogProps {
  kind: AssignmentKind
  id: string
  name: string
  /** Tag id in effect; null when a service inherits from its cluster. */
  current: number | null
  clusterName?: string
  /** The service's cluster tag, used for the inherit option. */
  clusterTag?: TagDTO
  onClose: () => void
}

/** Lets a global admin pick a tag for a cluster or service from its tag chip. */
export function EditTagDialog({ kind, id, name, current, clusterName, clusterTag, onClose }: EditTagDialogProps) {
  const queryClient = useQueryClient()
  const toast = useToast()
  const tags = useTags()
  const items = tags.data?.items ?? []
  // '' stands for inherit; only services have it.
  const [choice, setChoice] = useState(current === null ? '' : String(current))
  const shown = chosenTag(choice, items, clusterTag)

  const save = useMutation({
    mutationFn: () => saveAssignment(kind, id, assignedTagId(kind, choice, items)),
    onSuccess: () => {
      invalidateTagQueries(queryClient)
      toast.show(
        <>
          {name} is now {shown && <TagChip tag={shown} />}
        </>,
        'ok',
      )
      onClose()
    },
    onError: (caught) => toast.show(caught.message, 'bad'),
  })

  return (
    <Dialog
      title={`Tag for ${name}`}
      sub="Permissions follow the tag. Stored in Harbormaster only; AWS tags are not changed."
      onClose={onClose}
      actions={[
        { label: 'Cancel', onClick: onClose },
        { label: 'Save tag', kind: 'primary', onClick: () => save.mutate(), disabled: !tags.data || save.isPending },
      ]}
    >
      <div className={styles.radioList}>
        {kind === 'service' && (
          <label>
            <input type="radio" name="tag-choice" value="" checked={choice === ''} onChange={() => setChoice('')} />
            {clusterTag && <TagChip tag={{ ...clusterTag, inherited: true }} />}
            <span className={styles.small}>Inherit from cluster {clusterName}</span>
          </label>
        )}
        {items.map((tag) => (
          <label key={tag.id}>
            <input
              type="radio"
              name="tag-choice"
              value={tag.id}
              checked={choice === String(tag.id)}
              onChange={() => setChoice(String(tag.id))}
            />
            <TagChip tag={{ id: tag.id, name: tag.name, color: tag.color, inherited: false }} />
          </label>
        ))}
      </div>
      <p className={styles.hint}>
        Global admins create tags on the{' '}
        <Link className={styles.link} to="/admin/tags">
          Tags
        </Link>{' '}
        page.
      </p>
    </Dialog>
  )
}
