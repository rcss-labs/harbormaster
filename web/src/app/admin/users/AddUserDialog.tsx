import { useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { api } from '../../../api/client'
import type { CreateUserResponse } from '../../../api/types'
import { Dialog } from '../../../components/Dialog'
import styles from './users.module.css'

const USERNAME_PATTERN = /^[a-z][a-z0-9._-]{1,31}$/
const USERNAME_MESSAGE = 'Use 2–32 lowercase letters, digits, dots, hyphens or underscores.'

/** Creates a user, then shows the one-time temporary password in a follow-up dialog. */
export function AddUserDialog({ onClose, ssoOn = false }: { onClose: () => void; ssoOn?: boolean }) {
  const queryClient = useQueryClient()
  const [username, setUsername] = useState('')
  const [admin, setAdmin] = useState(false)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [created, setCreated] = useState<{ username: string; password: string } | null>(null)

  const submit = async () => {
    const name = username.trim().toLowerCase()
    if (!USERNAME_PATTERN.test(name)) {
      setError(USERNAME_MESSAGE)
      return
    }
    setError('')
    setBusy(true)
    try {
      const result = await api.post<CreateUserResponse>('/api/users', { username: name, admin })
      void queryClient.invalidateQueries({ queryKey: ['users'] })
      void queryClient.invalidateQueries({ queryKey: ['permissions'] })
      setCreated({ username: result.user.username, password: result.temporary_password })
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : 'Could not add the user.')
    } finally {
      setBusy(false)
    }
  }

  if (created) {
    return (
      <Dialog
        title={`Temporary password for ${created.username}`}
        sub="Shown once. Copy it now."
        actions={[{ label: 'Done', kind: 'primary', onClick: onClose }]}
        onClose={onClose}
      >
        <div className={styles.secret}>{created.password}</div>
        <p className={styles.hint}>They are asked to choose a new password at first sign-in.</p>
      </Dialog>
    )
  }

  return (
    <Dialog
      title="Add user"
      sub="They sign in with this username and password. Give permissions in the matrix afterwards."
      actions={[
        { label: 'Cancel', onClick: onClose },
        { label: 'Add user', kind: 'primary', onClick: () => void submit(), disabled: busy },
      ]}
      onClose={onClose}
    >
      <label className={styles.field}>
        <span>Username</span>
        <input
          className={`${styles.ctl} ${styles.ctlLg}`}
          placeholder="alex"
          autoComplete="off"
          value={username}
          onChange={(event) => setUsername(event.target.value)}
        />
      </label>
      <label className={styles.check}>
        <input type="checkbox" checked={admin} onChange={(event) => setAdmin(event.target.checked)} /> Global admin (can do
        everything)
      </label>
      <p className={styles.hint}>They are asked to choose a new password at first sign-in.</p>
      {ssoOn && (
        <p className={styles.hint}>
          New local users can only sign in with a password if you make them break-glass. SSO users are created automatically at
          their first sign-in, or set a pending link to attach one to an existing account.
        </p>
      )}
      <div className={styles.err} role="alert">
        {error}
      </div>
    </Dialog>
  )
}
