import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { Link } from 'react-router'
import { api } from '../../../api/client'
import { useAccounts, useTags } from '../../../api/hooks'
import type { AccountDTO } from '../../../api/types'
import { Ago } from '../../../components/Ago'
import { Card } from '../../../components/Card'
import { ConfirmDialog } from '../../../components/ConfirmDialog'
import { KV } from '../../../components/KV'
import { Pill } from '../../../components/Pill'
import { Table, tableClass } from '../../../components/Table'
import { TagChip } from '../../../components/TagChip'
import { useToast } from '../../../components/Toast'
import { useAuth } from '../../auth/AuthProvider'
import { ConnectAccountDialog } from './ConnectAccountDialog'
import { EditAccountDialog } from './EditAccountDialog'
import { invalidateAccountQueries } from './accountApi'
import styles from './accounts.module.css'

type DialogState = { kind: 'connect' } | { kind: 'edit' | 'disconnect'; account: AccountDTO } | null

/** Status pill per account: connected, error (with the last error as its title) or disabled. */
function StatusPill({ account }: { account: AccountDTO }) {
  if (account.status === 'error') return <Pill kind="bad" title={account.last_error}>Error</Pill>
  if (account.status === 'disabled') return <Pill kind="mute">Disabled</Pill>
  return <Pill kind="ok">Connected</Pill>
}

export function Accounts() {
  const { me } = useAuth()
  const isAdmin = me?.admin ?? false
  // Non-admins would get 403s, so the admin queries stay disabled for them.
  const accounts = useAccounts({ enabled: isAdmin })
  const tags = useTags({ enabled: isAdmin })
  const queryClient = useQueryClient()
  const toast = useToast()
  const [dialog, setDialog] = useState<DialogState>(null)

  const disconnect = useMutation({
    mutationFn: (account: AccountDTO) => api.del(`/api/accounts/${account.id}`),
    onSuccess: (_result, account) => {
      invalidateAccountQueries(queryClient)
      toast.show(
        <>
          Disconnected <strong>{account.name}</strong>.
        </>,
        'ok',
      )
      setDialog(null)
    },
    onError: (caught) => toast.show(caught.message, 'bad'),
  })

  if (!me || !isAdmin) return null

  const close = () => setDialog(null)
  const runtime = accounts.data?.runtime

  return (
    <>
      <div className={styles.pageHead}>
        <div className={styles.titles}>
          <h2>AWS accounts</h2>
          <div className={styles.meta}>Harbormaster runs as an ECS service and reaches each account by assuming an IAM role.</div>
        </div>
        <div className={styles.actions}>
          <button type="button" className={`${styles.btn} ${styles.primary}`} onClick={() => setDialog({ kind: 'connect' })}>
            Connect account
          </button>
        </div>
      </div>

      <div className={styles.stack}>
        {accounts.data && (
          <div className={styles.card}>
            <Table>
              <thead>
                <tr>
                  <th>Account</th>
                  <th>Role Harbormaster assumes</th>
                  <th>Regions scanned</th>
                  <th className={tableClass.num}>Clusters</th>
                  <th>Status</th>
                  <th>Last check</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {accounts.data.items.map((account) => (
                  <tr key={account.id}>
                    <td>
                      <div className={tableClass.name}>{account.name}</div>
                      <div className={`${styles.mono} ${styles.muted} ${styles.small}`}>{account.account_id}</div>
                    </td>
                    <td className={`${styles.mono} ${styles.small}`}>{account.role_arn}</td>
                    <td>{account.regions.join(', ')}</td>
                    <td className={tableClass.num}>{account.clusters}</td>
                    <td>
                      <StatusPill account={account} />
                    </td>
                    <td className={styles.muted}>{account.last_ok_at ? <Ago ts={account.last_ok_at} /> : '–'}</td>
                    <td>
                      <button type="button" className={`${styles.btn} ${styles.sm}`} onClick={() => setDialog({ kind: 'edit', account })}>
                        Edit
                      </button>{' '}
                      <button
                        type="button"
                        className={`${styles.btn} ${styles.sm} ${styles.danger}`}
                        onClick={() => setDialog({ kind: 'disconnect', account })}
                      >
                        Disconnect
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </Table>
          </div>
        )}

        {runtime && (
          <div className={styles.grid}>
            <Card title="Where Harbormaster runs">
              <KV
                rows={[
                  ['Cluster / service', <span className={styles.mono}>{`${runtime.cluster} / ${runtime.service}`}</span>],
                  [
                    'Updates',
                    `Polls ECS every ${runtime.intervals.services.replace(/s$/, '')} s per account and region; metrics every 60 s; VPCs and EFS every 5 min`,
                  ],
                  [
                    'Own data',
                    'PostgreSQL on RDS: users, sessions, tags, permissions, audit and operations logs. No AWS definitions are stored.',
                  ],
                ]}
              />
            </Card>
            {tags.data && (
              <Card title="Tags">
                <div className={styles.form}>
                  <p className={styles.hint}>
                    Tags are kept in Harbormaster only and never written to AWS. An untagged cluster is Dev; an untagged service
                    inherits its cluster's tag.
                  </p>
                  <div className={styles.toolbar}>
                    {tags.data.items.map((tag) => (
                      <span key={tag.id} className={styles.tagCount}>
                        <TagChip tag={{ id: tag.id, name: tag.name, color: tag.color, inherited: false }} />
                        <span className={`${styles.muted} ${styles.small}`}>{tag.services_effective} services</span>
                      </span>
                    ))}
                  </div>
                  <div>
                    <Link to="/settings/general" className={`${styles.btn} ${styles.sm}`}>
                      Manage tags
                    </Link>
                  </div>
                </div>
              </Card>
            )}
          </div>
        )}
      </div>

      {dialog?.kind === 'connect' && <ConnectAccountDialog onClose={close} />}
      {dialog?.kind === 'edit' && <EditAccountDialog account={dialog.account} onClose={close} />}
      {dialog?.kind === 'disconnect' && (
        <ConfirmDialog
          title="Disconnect account"
          sub={`Disconnect ${dialog.account.name}? Its clusters, services and tasks disappear from Harbormaster; tags and permissions are kept for 30 days.`}
          confirmLabel="Disconnect"
          danger
          onConfirm={() => disconnect.mutate(dialog.account)}
          onCancel={close}
        />
      )}
    </>
  )
}
