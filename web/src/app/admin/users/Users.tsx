import { useState } from 'react'
import { usePermissions, useUsers } from '../../../api/hooks'
import { AdminOnly } from '../../../components/AdminOnly'
import { useAuth } from '../../auth/AuthProvider'
import { useCrumbs } from '../../shell/crumbs'
import { AddUserDialog } from './AddUserDialog'
import { PermissionMatrix } from './PermissionMatrix'
import { UsersTable } from './UsersTable'
import styles from './users.module.css'

export function Users() {
  useCrumbs([['Users and permissions']])
  const { me } = useAuth()
  const isAdmin = me?.admin ?? false
  // Non-admins would get 403s, so the admin queries stay disabled for them.
  const users = useUsers({ enabled: isAdmin })
  const permissions = usePermissions({ enabled: isAdmin })
  const [adding, setAdding] = useState(false)

  if (!me || !isAdmin) return <AdminOnly area="users" />

  return (
    <>
      <div className={styles.pageHead}>
        <div className={styles.titles}>
          <h1>Users and permissions</h1>
          <div className={styles.meta}>
            Global admins can do everything. Every other user gets permissions per tag. Sign-in is username and password.
          </div>
        </div>
        <div className={styles.actions}>
          <button type="button" className={`${styles.btn} ${styles.primary}`} onClick={() => setAdding(true)}>
            Add user
          </button>
        </div>
      </div>
      <div className={styles.stack}>
        {permissions.data && <PermissionMatrix data={permissions.data} />}
        {users.data && <UsersTable users={users.data.items} meId={me.id} />}
      </div>
      {adding && <AddUserDialog onClose={() => setAdding(false)} />}
    </>
  )
}
