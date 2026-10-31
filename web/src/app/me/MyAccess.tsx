import { ACTIONS, VIEW_BIT, bitOf } from '../admin/perms'
import { Table, tableClass } from '../../components/Table'
import { TagChip } from '../../components/TagChip'
import { useAuth } from '../auth/AuthProvider'
import { useCrumbs } from '../shell/crumbs'
import styles from './myAccess.module.css'

/** The signed-in user's permissions on each tag. Global admins can do everything, so they get no table. */
export function MyAccess() {
  useCrumbs([['My access']])
  const { me } = useAuth()
  if (!me) return null

  if (me.admin) {
    return (
      <div className={styles.pageHead}>
        <div className={styles.titles}>
          <h1>My access</h1>
          <div className={styles.meta}>You are a global admin: you can do everything, on every tag.</div>
        </div>
      </div>
    )
  }

  return (
    <>
      <div className={styles.pageHead}>
        <div className={styles.titles}>
          <h1>My access</h1>
          <div className={styles.meta}>What you can do on each tag. Ask a global admin to change it.</div>
        </div>
      </div>
      <div className={styles.card}>
        <Table>
          <thead>
            <tr>
              <th>Tag</th>
              {ACTIONS.map(([key, label]) => (
                <th key={key}>{label}</th>
              ))}
              <th className={tableClass.num}>Services</th>
            </tr>
          </thead>
          <tbody>
            {me.tags.map((tag) => {
              const granted = me.perms[tag.id] ?? 0
              // Every action needs View on the same tag, matching the server's rule.
              const canView = (granted & VIEW_BIT) !== 0
              return (
                <tr key={tag.id}>
                  <td>
                    <TagChip tag={{ id: tag.id, name: tag.name, color: tag.color, inherited: false }} />
                  </td>
                  {ACTIONS.map(([key], index) =>
                    canView && (granted & bitOf(index)) !== 0 ? (
                      <td key={key} className={styles.yes}>
                        ✓
                      </td>
                    ) : (
                      <td key={key} className={styles.no}>
                        –
                      </td>
                    ),
                  )}
                  <td className={tableClass.num}>{tag.services_in_effect}</td>
                </tr>
              )
            })}
          </tbody>
        </Table>
      </div>
    </>
  )
}
