import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useState, type ReactNode } from 'react'
import { api } from '../../../api/client'
import type { UserDTO } from '../../../api/types'
import { Ago } from '../../../components/Ago'
import { Panel } from '../../../components/Panel'
import { ConfirmDialog } from '../../../components/ConfirmDialog'
import { Popover } from '../../../components/Popover'
import { Pill } from '../../../components/Pill'
import { Table, tableClass } from '../../../components/Table'
import { TagChip } from '../../../components/TagChip'
import { useToast } from '../../../components/Toast'
import { useAuth } from '../../auth/AuthProvider'
import { PendingLinkDialog } from './PendingLinkDialog'
import styles from './users.module.css'

const expiry = (iso: string | null) => (iso ? new Date(iso).toLocaleDateString() : 'soon')

type Pending = { kind: 'reset' | 'admin' | 'delete' | 'breakglass' | 'unlink'; user: UserDTO }

function MenuItem({ children, danger = false, disabled = false, title, onClick }: { children: ReactNode; danger?: boolean; disabled?: boolean; title?: string; onClick: () => void }) {
  return (
    <button type="button" role="menuitem" className={`${styles.menuItem} ${danger ? styles.menuDanger : ''}`.trim()} disabled={disabled} title={title} onClick={onClick}>
      {children}
    </button>
  )
}

export function UsersTable({ users, meId, ssoConfigured = false }: { users: UserDTO[]; meId: number; ssoConfigured?: boolean }) {
  const queryClient = useQueryClient()
  const toast = useToast()
  const auth = useAuth()
  const [pending, setPending] = useState<Pending | null>(null)
  const [linking, setLinking] = useState<UserDTO | null>(null)
  const [menuFor, setMenuFor] = useState<number | null>(null)
  /** Runs a menu choice and closes the menu first. */
  const choose = (action: () => void) => {
    setMenuFor(null)
    action()
  }

  const refresh = () => {
    void queryClient.invalidateQueries({ queryKey: ['users'] })
    void queryClient.invalidateQueries({ queryKey: ['permissions'] })
  }
  // Acting on yourself changes /api/me (sidebar link, hints); other users' changes do not.
  const refreshMe = (user: UserDTO) => {
    if (user.id === meId) void auth.refresh()
  }
  const refreshSSO = () => void queryClient.invalidateQueries({ queryKey: ['sso'] })
  const fail = (error: Error) => toast.show(error.message, 'bad')

  const showTemporaryPassword = (username: string, password: string) =>
    toast.show(
      <>
        New temporary password for {username}: <code>{password}</code>
      </>,
      'info',
      { sticky: true },
    )

  const reset = useMutation({
    mutationFn: (user: UserDTO) =>
      api.post<{ temporary_password: string }>(`/api/users/${user.id}/reset-password`).then((body) => ({ user, body })),
    onSuccess: ({ user, body }) => {
      showTemporaryPassword(user.username, body.temporary_password)
      refresh()
      refreshSSO()
      refreshMe(user)
    },
    onError: fail,
  })
  const setBreakGlass = useMutation({
    mutationFn: (user: UserDTO) => api.post(`/api/users/${user.id}/break-glass`, { break_glass: !user.break_glass }),
    onSuccess: (_result, user) => {
      refresh()
      refreshSSO()
      refreshMe(user)
    },
    onError: (error) => {
      fail(error)
      refresh()
    },
  })
  const clearLink = useMutation({
    mutationFn: (user: UserDTO) => api.del(`/api/users/${user.id}/sso-link`),
    onSuccess: (_result, user) => {
      toast.show('Pending link cleared.')
      refresh()
      refreshMe(user)
    },
    onError: (error) => {
      fail(error)
      refresh()
    },
  })
  const unlink = useMutation({
    mutationFn: (user: UserDTO) =>
      api.post<{ temporary_password?: string }>(`/api/users/${user.id}/sso-unlink`).then((body) => ({ user, body })),
    onSuccess: ({ user, body }) => {
      if (body?.temporary_password) showTemporaryPassword(user.username, body.temporary_password)
      refresh()
      refreshSSO()
      refreshMe(user)
    },
    onError: (error) => {
      fail(error)
      refresh()
    },
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
    if (kind === 'breakglass') setBreakGlass.mutate(user)
    if (kind === 'unlink') unlink.mutate(user)
  }

  return (
    <Panel title="Users" flush actions={<span>{users.length} users</span>}>
      <Table>
        <thead>
          <tr>
            <th>Username</th>
            <th>Sign-in</th>
            <th>Role</th>
            <th>Can see</th>
            <th>Last sign-in</th>
            <th>Actions</th>
          </tr>
        </thead>
        <tbody>
          {users.map((user) => (
            <tr key={user.id}>
              <td className={tableClass.name} title={user.display_name ?? undefined}>
                {user.username}
                {user.id === meId && <> <span className={`${styles.muted} ${styles.small}`}>(you)</span></>}
                {user.email && <div className={`${styles.muted} ${styles.small}`}>{user.email}</div>}
              </td>
              <td>
                <span className={styles.chips}>
                  {user.has_password && <Pill kind="mute">Password</Pill>}
                  {user.sso === 'linked' && <Pill kind="info">SSO linked</Pill>}
                  {user.sso === 'pending' && (
                    <Pill kind="warn" title={`Pending link to ${user.sso_name_id}, expires ${expiry(user.pending_link_expires_at)}`}>
                      Link pending
                    </Pill>
                  )}
                  {user.sso === 'pending_expired' && <Pill kind="mute">Link expired</Pill>}
                  {user.break_glass && <Pill kind="warn">Break-glass</Pill>}
                </span>
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
                <div className={styles.menuWrap}>
                  <button
                    type="button"
                    className={`${styles.btn} ${styles.sm}`}
                    aria-label={`Actions for ${user.username}`}
                    aria-haspopup="menu"
                    aria-expanded={menuFor === user.id}
                    onClick={() => setMenuFor((current) => (current === user.id ? null : user.id))}
                  >
                    Actions
                  </button>
                  <Popover open={menuFor === user.id} onClose={() => setMenuFor(null)} label={`Actions for ${user.username}`} role="menu" align="right" width={220}>
                    <div className={styles.menu}>
                      <MenuItem onClick={() => choose(() => setPending({ kind: 'reset', user }))}>
                        {user.has_password ? 'Reset password' : 'Set password'}
                      </MenuItem>
                      <MenuItem
                        disabled={!user.has_password}
                        title={user.has_password ? undefined : `Set a password for ${user.username} first.`}
                        onClick={() => choose(() => setPending({ kind: 'breakglass', user }))}
                      >
                        {user.break_glass ? 'Remove break-glass' : 'Make break-glass'}
                      </MenuItem>
                      {ssoConfigured && (user.sso === 'none' || user.sso === 'pending_expired' || user.sso === 'pending') && (
                        <MenuItem onClick={() => choose(() => setLinking(user))}>
                          {user.sso === 'pending' ? 'Change pending link…' : 'Link SSO…'}
                        </MenuItem>
                      )}
                      {user.sso === 'pending' && (
                        <MenuItem onClick={() => choose(() => clearLink.mutate(user))}>Clear pending link</MenuItem>
                      )}
                      {user.sso === 'linked' && (
                        <MenuItem onClick={() => choose(() => setPending({ kind: 'unlink', user }))}>Unlink SSO…</MenuItem>
                      )}
                      <MenuItem onClick={() => choose(() => setPending({ kind: 'admin', user }))}>
                        {user.admin ? 'Remove global admin' : 'Make global admin'}
                      </MenuItem>
                      <MenuItem
                        danger
                        disabled={user.id === meId}
                        title={user.id === meId ? 'You cannot delete yourself' : undefined}
                        onClick={() => choose(() => setPending({ kind: 'delete', user }))}
                      >
                        Delete
                      </MenuItem>
                    </div>
                  </Popover>
                </div>
              </td>
            </tr>
          ))}
          {/* Room for the last rows' menus, which the scrolling table wrapper would otherwise clip. */}
          {menuFor !== null && (
            <tr aria-hidden="true">
              <td colSpan={6} className={styles.menuRoom} />
            </tr>
          )}
        </tbody>
      </Table>
      {pending?.kind === 'reset' && (
        <ConfirmDialog
          title={pending.user.has_password ? `Reset the password of ${pending.user.username}?` : `Set a password for ${pending.user.username}?`}
          sub={
            pending.user.has_password
              ? 'They get a new temporary password and must choose their own at next sign-in. Their sessions end now.'
              : 'They get a temporary password and must choose their own at next sign-in. While single sign-on is on, only break-glass accounts can use it.'
          }
          confirmLabel={pending.user.has_password ? 'Reset password' : 'Set password'}
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
      {pending?.kind === 'breakglass' && (
        <ConfirmDialog
          title={pending.user.break_glass ? `Remove break-glass from ${pending.user.username}?` : `Make ${pending.user.username} a break-glass account?`}
          sub={
            pending.user.break_glass
              ? undefined
              : 'Break-glass accounts can sign in with a password even when single sign-on is on. Keep this to a few global admins.'
          }
          confirmLabel={pending.user.break_glass ? 'Remove break-glass' : 'Make break-glass'}
          onConfirm={confirm}
          onCancel={() => setPending(null)}
        />
      )}
      {pending?.kind === 'unlink' && (
        <ConfirmDialog
          title={`Unlink ${pending.user.username} from SSO?`}
          sub={
            pending.user.has_password
              ? 'Their SSO sessions end now. They keep their password.'
              : 'Their SSO sessions end now. They get a temporary password and must choose their own at next sign-in.'
          }
          confirmLabel="Unlink"
          onConfirm={confirm}
          onCancel={() => setPending(null)}
        />
      )}
      {linking && <PendingLinkDialog user={linking} onClose={() => setLinking(null)} />}
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
    </Panel>
  )
}
