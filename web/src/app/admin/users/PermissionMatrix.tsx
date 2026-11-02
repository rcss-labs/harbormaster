import type { PermissionsDTO } from '../../../api/types'
import { Panel } from '../../../components/Panel'
import { Pill } from '../../../components/Pill'
import { Table } from '../../../components/Table'
import { TagChip } from '../../../components/TagChip'
import { ACTIONS } from '../perms'
import { PermCell } from './PermCell'
import styles from './users.module.css'

export function PermissionMatrix({ data }: { data: PermissionsDTO }) {
  const legend = (
    <span className={styles.legend}>
      {ACTIONS.map(([key, name, letter]) => (
        <span key={key}>
          <b className={styles.mono}>{letter}</b> {name}
        </span>
      ))}
    </span>
  )
  return (
    <Panel title="Users & permissions" actions={legend}>
      <Table>
        <thead>
          <tr>
            <th>User</th>
            {data.tags.map((tag) => (
              <th key={tag.id}>
                <TagChip tag={{ id: tag.id, name: tag.name, color: tag.color, inherited: false }} />{' '}
                <span className={`${styles.muted} ${styles.small}`}>{tag.services_in_effect} services</span>
              </th>
            ))}
          </tr>
        </thead>
        <tbody className={styles.matrix}>
          {data.users.map((user) => (
            <tr key={user.id}>
              <td>
                <div className={styles.name}>{user.username}</div>
                <div className={`${styles.muted} ${styles.small}`}>{user.admin ? 'Global admin' : 'User'}</div>
              </td>
              {user.admin ? (
                <td colSpan={data.tags.length}>
                  <Pill kind="info">Everything, on every tag</Pill>
                </td>
              ) : (
                data.tags.map((tag) => (
                  <td key={tag.id}>
                    <PermCell
                      userId={user.id}
                      username={user.username}
                      tagId={tag.id}
                      tagName={tag.name}
                      actions={user.perms[String(tag.id)] ?? 0}
                    />
                  </td>
                ))
              )}
            </tr>
          ))}
        </tbody>
      </Table>
      <p className={styles.hint}>
        Turning on any action also turns on View. Turning off View removes everything on that tag. A user sees a cluster when
        they can view at least one of its services.
      </p>
    </Panel>
  )
}
