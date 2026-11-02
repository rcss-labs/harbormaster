import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { api } from '../../../api/client'
import type { UserDTO } from '../../../api/types'
import { Dialog } from '../../../components/Dialog'
import styles from './users.module.css'

/** Sets (or replaces) the pending SSO link of a local user. Conflicts (already linked, NameID taken) show inline. */
export function PendingLinkDialog({ user, onClose }: { user: UserDTO; onClose: () => void }) {
  const queryClient = useQueryClient()
  const [nameId, setNameId] = useState(user.sso === 'pending' ? (user.sso_name_id ?? '') : '')

  const save = useMutation({
    mutationFn: (value: string) => api.put(`/api/users/${user.id}/sso-link`, { name_id: value }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['users'] })
      onClose()
    },
  })
  const trimmed = nameId.trim()

  return (
    <Dialog
      title={`Link ${user.username} to an SSO identity`}
      sub={`At their next SSO sign-in with this NameID, ${user.username} keeps this account and its permissions instead of getting a new one. The link expires in 14 days.`}
      actions={[
        { label: 'Cancel', onClick: onClose },
        {
          label: 'Set pending link',
          kind: 'primary',
          disabled: trimmed === '' || trimmed.length > 256 || save.isPending,
          onClick: () => save.mutate(trimmed),
        },
      ]}
      onClose={onClose}
    >
      <label className={styles.field}>
        <span>NameID</span>
        <input
          className={`${styles.ctl} ${styles.ctlLg}`}
          autoComplete="off"
          maxLength={256}
          value={nameId}
          onChange={(event) => setNameId(event.target.value)}
        />
      </label>
      <p className={styles.hint}>Copy it exactly as the identity provider issues it. Test sign-in shows it.</p>
      <div className={styles.err} role="alert">
        {save.error?.message}
      </div>
    </Dialog>
  )
}
