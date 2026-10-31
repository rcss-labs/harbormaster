import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '../../../api/client'
import type { TagImpactDTO, TagItemDTO } from '../../../api/types'
import { Dialog } from '../../../components/Dialog'
import { useToast } from '../../../components/Toast'
import styles from './tags.module.css'
import { invalidateTagQueries } from './tagApi'

interface DeleteTagDialogProps {
  tag: TagItemDTO
  onClose: () => void
}

/** Shows what deleting the tag changes, from the impact endpoint, and deletes it on confirm. */
export function DeleteTagDialog({ tag, onClose }: DeleteTagDialogProps) {
  const queryClient = useQueryClient()
  const toast = useToast()
  const impact = useQuery({
    queryKey: ['tag-impact', tag.id],
    queryFn: () => api.get<TagImpactDTO>(`/api/tags/${tag.id}/impact`),
  })

  const remove = useMutation({
    mutationFn: () => api.del(`/api/tags/${tag.id}`),
    onSuccess: () => {
      invalidateTagQueries(queryClient)
      onClose()
    },
    onError: (caught) => toast.show(caught.message, 'bad'),
  })

  const data = impact.data
  return (
    <Dialog
      title={`Delete tag ${tag.name}?`}
      sub="This changes who can see and do what."
      onClose={onClose}
      actions={[
        { label: 'Cancel', onClick: onClose },
        { label: 'Delete tag', kind: 'danger', onClick: () => remove.mutate(), disabled: remove.isPending },
      ]}
    >
      {data && (
        <ul className={styles.impact}>
          <li>{clusterLine(data.clusters.length)}</li>
          <li>{serviceLine(data.services.length)}</li>
          <li>{userLine(data.users, tag.name)}</li>
        </ul>
      )}
    </Dialog>
  )
}

function clusterLine(count: number): string {
  return `${count} ${count === 1 ? 'cluster becomes' : 'clusters become'} Dev.`
}

function serviceLine(count: number): string {
  return `${count} ${count === 1 ? 'service goes' : 'services go'} back to inheriting their cluster's tag.`
}

function userLine(usernames: string[], tagName: string): string {
  const names = usernames.length ? `: ${usernames.join(', ')}` : ''
  return `${usernames.length} ${usernames.length === 1 ? 'user loses' : 'users lose'} the permissions they had on ${tagName}${names}.`
}
