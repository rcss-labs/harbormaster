import type { ReactNode } from 'react'
import { ACTIONS, VIEW_BIT, bitOf } from '../admin/perms'
import { KV } from '../../components/KV'
import { Panel } from '../../components/Panel'
import { usePhone } from '../../components/usePhone'
import { Table, tableClass } from '../../components/Table'
import { TagChip } from '../../components/TagChip'
import { useAuth } from '../auth/AuthProvider'
import { useCrumbs } from '../shell/crumbs'
import styles from './myAccess.module.css'

/** The signed-in user's permissions on each tag. Global admins can do everything, so they get no table. */
export function MyAccess() {
  useCrumbs([['My access']])
  const { me } = useAuth()
  const phone = usePhone()
  if (!me) return null

  if (me.admin) {
    return (
      <div className={styles.pageHead}>
        <div className={styles.titles}>
          <h1 className="hm-phone-title-sr">My access</h1>
          <div className={styles.meta}>You are a global admin: you can do everything, on every tag.</div>
        </div>
      </div>
    )
  }

  const granted = (tagId: number) => me.perms[tagId] ?? 0
  const allowed = (tagId: number, index: number) => (granted(tagId) & VIEW_BIT) !== 0 && (granted(tagId) & bitOf(index)) !== 0

  return (
    <>
      <div className={styles.pageHead}>
        <div className={styles.titles}>
          <h1 className="hm-phone-title-sr">My access</h1>
          <div className={styles.meta}>What you can do on each tag. Ask a global admin to change it.</div>
        </div>
      </div>
      {phone ? (
        <div className={styles.tagPanels}>
          {me.tags.map((tag) => (
            <Panel
              key={tag.id}
              label={`Permissions on ${tag.name}`}
              title={<TagChip tag={{ id: tag.id, name: tag.name, color: tag.color, inherited: false }} />}
              actions={<span>{tag.services_in_effect} services</span>}
            >
              <KV
                rows={[
                  ...ACTIONS.map(([key, label], index): [string, ReactNode] => [
                    label,
                    <span key={key} className={allowed(tag.id, index) ? styles.yes : styles.no}>
                      {allowed(tag.id, index) ? '✓' : '–'}
                    </span>,
                  ]),
                  ['Services', <span className={styles.mono}>{tag.services_in_effect}</span>],
                ]}
              />
            </Panel>
          ))}
        </div>
      ) : (
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
      )}
    </>
  )
}
