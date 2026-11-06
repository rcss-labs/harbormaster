import { useState } from 'react'
import { useAuthOptions, usePermissions, useSSOConfig, useUsers } from '../../../api/hooks'
import { usePhone } from '../../../components/useNarrow'
import { useAuth } from '../../auth/AuthProvider'
import { HeaderAction } from '../../settings/HeaderAction'
import { AddUserDialog } from './AddUserDialog'
import { PermissionMatrix } from './PermissionMatrix'
import { UsersTable } from './UsersTable'
import styles from './users.module.css'

export function Users() {
  const { me } = useAuth()
  const phone = usePhone()
  const isAdmin = me?.admin ?? false
  // Non-admins would get 403s, so the admin queries stay disabled for them.
  const users = useUsers({ enabled: isAdmin })
  const permissions = usePermissions({ enabled: isAdmin })
  const ssoOn = useAuthOptions().data?.sso?.enabled ?? false
  const ssoConfigured = useSSOConfig({ enabled: isAdmin }).data?.configured ?? false
  const [adding, setAdding] = useState(false)

  if (!me || !isAdmin) return null

  return (
    <>
      <HeaderAction>
        <button type="button" className={`${styles.btn} ${styles.primary}`} onClick={() => setAdding(true)}>
          Add user
        </button>
      </HeaderAction>
      <p className={styles.meta}>
        Global admins can do everything. Every other user gets permissions per tag.{' '}
        {ssoOn ? 'Sign-in is single sign-on; break-glass accounts can also use a password.' : 'Sign-in is username and password.'}
      </p>
      <div className={styles.stack}>
        {permissions.data && !phone && <PermissionMatrix data={permissions.data} />}
        {users.data && <UsersTable users={users.data.items} meId={me.id} ssoConfigured={ssoConfigured} permissions={permissions.data} />}
      </div>
      {adding && <AddUserDialog ssoOn={ssoOn} onClose={() => setAdding(false)} />}
    </>
  )
}
