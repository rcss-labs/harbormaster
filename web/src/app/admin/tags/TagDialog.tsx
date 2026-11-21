import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { api } from '../../../api/client'
import type { TagColor, TagItemDTO } from '../../../api/types'
import { Dialog } from '../../../components/Dialog'
import { Toggle } from '../../../components/Toggle'
import { useToast } from '../../../components/Toast'
import styles from './tags.module.css'
import { TAG_COLORS, TAG_NAME_MESSAGE, TAG_NAME_PATTERN, invalidateTagQueries } from './tagApi'

interface TagDialogProps {
  /** The tag being renamed; omitted when creating. */
  existing?: TagItemDTO
  onClose: () => void
}

/** Creates a tag, or renames one and recolours it. Server errors (for example a duplicate name) show inside the dialog. */
export function TagDialog({ existing, onClose }: TagDialogProps) {
  const queryClient = useQueryClient()
  const toast = useToast()
  const [name, setName] = useState(existing?.name ?? '')
  const [color, setColor] = useState<TagColor>(existing?.color ?? 'blue')
  const [nonProduction, setNonProduction] = useState(existing?.non_production ?? false)
  const [error, setError] = useState('')

  const save = useMutation({
    mutationFn: () => {
      const body = { name: name.trim(), color, non_production: nonProduction }
      return existing ? api.patch<void>(`/api/tags/${existing.id}`, body) : api.post<void>('/api/tags', body)
    },
    onSuccess: () => {
      invalidateTagQueries(queryClient)
      toast.show(
        existing ? 'Tag saved.' : (
          <>
            Created tag <strong>{name.trim()}</strong>. Assign it on this page.
          </>
        ),
        'ok',
      )
      onClose()
    },
    onError: (caught) => setError(caught.message),
  })

  const submit = () => {
    if (!TAG_NAME_PATTERN.test(name.trim())) {
      setError(TAG_NAME_MESSAGE)
      return
    }
    setError('')
    save.mutate()
  }

  return (
    <Dialog
      title={existing ? `Edit tag ${existing.name}` : 'Create tag'}
      sub={
        existing
          ? 'Clusters, services and permissions keep pointing at it.'
          : 'New tags start with no users. Only global admins can see resources with it until you give permissions.'
      }
      onClose={onClose}
      actions={[
        { label: 'Cancel', onClick: onClose },
        {
          label: existing ? 'Save' : 'Create tag',
          kind: 'primary',
          onClick: submit,
          disabled: save.isPending,
        },
      ]}
    >
      <label className={styles.field}>
        <span>Name</span>
        <input
          className={`${styles.ctl} ${styles.ctlLg}`}
          placeholder="QA"
          autoComplete="off"
          value={name}
          disabled={existing?.builtin}
          onChange={(event) => setName(event.target.value)}
        />
      </label>
      <div className={styles.field}>
        <span>Color</span>
        <div className={styles.toolbar}>
          {TAG_COLORS.map((option) => (
            <label key={option} className={styles.toolbar}>
              <input type="radio" name="tag-color" value={option} checked={color === option} onChange={() => setColor(option)} />
              <span className={`${styles.chipLabel} ${styles[`c-${option}`]}`}>{option}</span>
            </label>
          ))}
        </div>
      </div>
      <Toggle checked={nonProduction} onChange={setNonProduction}>
        Non-production
      </Toggle>
      <p className={styles.hint}>Services under this tag can be suggested for Fargate Spot.</p>
      <div className={styles.err} role="alert">
        {error}
      </div>
    </Dialog>
  )
}
