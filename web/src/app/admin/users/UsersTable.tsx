import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { api } from '../../../api/client'
import type { UserDTO } from '../../../api/types'
import { Ago } from '../../../components/Ago'
import { Card } from '../../../components/Card'
import { ConfirmDialog } from '../../../components/ConfirmDialog'
import { Pill } from '../../../components/Pill'
import { Table, tableClass } from '../../../components/Table'
import { TagChip } from '../../../components/TagChip'
import { useToast } from '../../../components/Toast'
import styles from './users.module.css'

type Pending = { kind: 'reset' | 'admin' | 'delete'; user: UserDTO }

export function UsersTable({ users, meId }: { users: UserDTO[]; meId: number }) {
  const queryClient = useQueryClient()
  const toast = useToast()
  const [pending, setPending] = useState<Pending | null>(null)

  const refresh = () => {
    void queryClient.invalidateQueries({ queryKey: ['users'] })
    void queryClient.invalidateQueries({ queryKey: ['permissions'] })
  }
  const fail = (error: Error) => toast.show(error.message, 'bad')

  const reset = useMutation({
    mutationFn: (user: UserDTO) =>
      api.post<{ temporary_password: string }>(`/api/users/${user.id}/reset-password`).then((body) => ({ user, body })),
    onSuccess: ({ user, body }) => {
      toast.show(
        <>
          New temporary password for {user.username}: <code>{body.temporary_password}</code>
        </>,
        'info',
        { sticky: true },
      )
      refresh()
    },
    onError: fail,
  })
  const setAdmin = useMutation({
    mutationFn: (user: UserDTO) => api.post(`/api/users/${user.id}/admin`, { admin: !user.admin }),
    onSuccess: refresh,
    onError: (error) => {
      fail(error)
      refresh()
    },
  })
  const remove = useMutation({
    mutationFn: (user: UserDTO) => api.del(`/api/users/${user.id}`),
    onSuccess: refresh,
    onError: fail,
  })

  const confirm = () => {
    if (!pending) return
    const { kind, user } = pending
    setPending(null)
    if (kind === 'reset') reset.mutate(user)
    if (kind === 'admin') setAdmin.mutate(user)
    if (kind === 'delete') remove.mutate(user)
  }

  return (
    <Card title="Users" right={<span className={`${styles.muted} ${styles.small}`}>{users.length} users</span>}>
      <Table>
        <thead>
          <tr>
            <th>Username</th>
            <th>Role</th>
            <th>Can see</th>
            <th>Last sign-in</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          {users.map((user) => (
            <tr key={user.id}>
              <td className={tableClass.name}>
                {user.username}
                {user.id === meId && <> <span className={`${styles.muted} ${styles.small}`}>(you)</span></>}
              </td>
              <td>{user.admin ? <Pill kind="info">Global admin</Pill> : <Pill kind="mute">User</Pill>}</td>
              <td>
                {user.admin ? (
                  <span className={styles.muted}>Everything</span>
                ) : user.can_see.length === 0 ? (
                  <span className={styles.muted}>Nothing yet</span>
                ) : (
                  <span className={styles.chips}>
                    {user.can_see.map((tag) => (
                      <TagChip key={tag.id} tag={tag} />
                    ))}
                  </span>
                )}
              </td>
              <td className={styles.muted}>{user.last_sign_in_at ? <Ago ts={user.last_sign_in_at} /> : 'never'}</td>
              <td className={tableClass.num}>
                <button type="button" className={`${styles.btn} ${styles.sm}`} onClick={() => setPending({ kind: 'reset', user })}>
                  Reset password
                </button>{' '}
                <button type="button" className={`${styles.btn} ${styles.sm}`} onClick={() => setPending({ kind: 'admin', user })}>
                  {user.admin ? 'Remove global admin' : 'Make global admin'}
                </button>{' '}
                <button
                  type="button"
                  className={`${styles.btn} ${styles.sm} ${styles.danger}`}
                  disabled={user.id === meId}
                  title={user.id === meId ? 'You cannot delete yourself' : undefined}
                  onClick={() => setPending({ kind: 'delete', user })}
                >
                  Delete
                </button>
              </td>
            </tr>
          ))}
        </tbody>
      </Table>
      {pending?.kind === 'reset' && (
        <ConfirmDialog
          title={`Reset the password of ${pending.user.username}?`}
          sub="They get a new temporary password and must choose their own at next sign-in. Their sessions end now."
          confirmLabel="Reset password"
          onConfirm={confirm}
          onCancel={() => setPending(null)}
        />
      )}
      {pending?.kind === 'admin' && (
        <ConfirmDialog
          title={pending.user.admin ? `Remove global admin from ${pending.user.username}?` : `Make ${pending.user.username} a global admin?`}
          sub={
            pending.user.admin
              ? 'They keep only the per-tag permissions in the matrix.'
              : 'Global admins can do everything on every tag, and manage users, tags and AWS accounts.'
          }
          confirmLabel={pending.user.admin ? 'Remove global admin' : 'Make global admin'}
          onConfirm={confirm}
          onCancel={() => setPending(null)}
        />
      )}
      {pending?.kind === 'delete' && (
        <ConfirmDialog
          danger
          title={`Delete ${pending.user.username}?`}
          sub="Their sessions end now. Their entries in operations and audit logs stay."
          confirmLabel="Delete user"
          onConfirm={confirm}
          onCancel={() => setPending(null)}
        />
      )}
    </Card>
  )
}
