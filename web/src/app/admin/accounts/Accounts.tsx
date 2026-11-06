import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { Link } from 'react-router'
import { api } from '../../../api/client'
import { useAccounts, usePollerStatus, useTags } from '../../../api/hooks'
import type { AccountDTO, PollerEntryDTO, RegionResult } from '../../../api/types'
import { Ago } from '../../../components/Ago'
import { ConfirmDialog } from '../../../components/ConfirmDialog'
import { DataTable, type DataTableColumn } from '../../../components/DataTable'
import { KV } from '../../../components/KV'
import { Panel } from '../../../components/Panel'
import { StatusDot } from '../../../components/StatusDot'
import { TagChip } from '../../../components/TagChip'
import { useToast } from '../../../components/Toast'
import { useAuth } from '../../auth/AuthProvider'
import { HeaderAction } from '../../settings/HeaderAction'
import { ConnectAccountDialog } from './ConnectAccountDialog'
import { EditAccountDialog } from './EditAccountDialog'
import { invalidateAccountQueries } from './accountApi'
import styles from './accounts.module.css'

type DialogState = { kind: 'connect' } | { kind: 'edit' | 'disconnect'; account: AccountDTO } | null

const minutesSince = (iso: string) => Math.max(1, Math.floor((Date.now() - Date.parse(iso)) / 60_000))

/** Status of one account: its own state, then what the poller reports for its regions. Raw AWS errors stay behind a disclosure. */
function AccountStatus({ account, polled, tested }: { account: AccountDTO; polled: PollerEntryDTO[]; tested: RegionResult[] | undefined }) {
  const failing = polled.filter((entry) => entry.failing)
  const throttled = polled.some((entry) => entry.throttled)
  const hasDetail = (account.status === 'error' && account.last_error !== '') || failing.length > 0 || (tested?.length ?? 0) > 0
  return (
    <>
      {account.status === 'error' ? (
        <StatusDot kind="bad">Error</StatusDot>
      ) : account.status === 'disabled' ? (
        <StatusDot kind="mute">Disabled</StatusDot>
      ) : (
        <StatusDot kind="ok">Connected</StatusDot>
      )}
      {failing.map((entry) => (
        <div key={entry.region} className={styles.statusNote} title={entry.region}>
          {`Data is ${entry.last_ok_at ? `${minutesSince(entry.last_ok_at)} min old` : 'unavailable'}`}
        </div>
      ))}
      {throttled && <div className={`${styles.statusNote} ${styles.throttled}`}>Slowed by AWS throttling</div>}
      {hasDetail && (
        <details className={styles.errorDetails}>
          <summary>Show AWS error</summary>
          <div className={styles.errorBody}>
            {account.status === 'error' && account.last_error !== '' && <div className={styles.mono}>{account.last_error}</div>}
            {failing.map((entry) => (
              <div key={entry.region} className={styles.mono}>
                {entry.last_error}
              </div>
            ))}
            {tested && tested.length > 0 && (
              <ul className={styles.testResults} aria-label="Last test">
                {tested.map((result) => (
                  <li key={result.region} className={result.ok ? styles.resultOk : styles.resultBad}>
                    {result.ok ? `${result.region} · ${result.clusters} ${result.clusters === 1 ? 'cluster' : 'clusters'}` : `${result.region} · ${result.error}`}
                  </li>
                ))}
              </ul>
            )}
          </div>
        </details>
      )}
    </>
  )
}

const describeRegion = (result: RegionResult) =>
  result.ok ? `${result.region}: ${result.clusters} ${result.clusters === 1 ? 'cluster' : 'clusters'}` : `${result.region}: ${result.error}`

export function Accounts() {
  const { me } = useAuth()
  const isAdmin = me?.admin ?? false
  // Non-admins would get 403s, so the admin queries stay disabled for them.
  const accounts = useAccounts({ enabled: isAdmin })
  const tags = useTags({ enabled: isAdmin })
  const poller = usePollerStatus()
  const queryClient = useQueryClient()
  const toast = useToast()
  const [dialog, setDialog] = useState<DialogState>(null)
  const [testResults, setTestResults] = useState<Record<number, RegionResult[]>>({})

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

  const testNow = useMutation({
    mutationFn: (account: AccountDTO) =>
      api
        .post<{ regions: RegionResult[] }>('/api/accounts/test', {
          role_arn: account.role_arn,
          external_id: account.external_id,
          regions: account.regions,
        })
        .then((body) => ({ account, regions: body.regions })),
    onSuccess: ({ account, regions }) => {
      setTestResults((current) => ({ ...current, [account.id]: regions }))
      const allOk = regions.every((region) => region.ok)
      toast.show(`${account.name}: ${regions.map(describeRegion).join('; ')}`, allOk ? 'ok' : 'bad')
      void queryClient.invalidateQueries({ queryKey: ['poller'] })
    },
    onError: (caught) => toast.show(caught.message, 'bad'),
  })

  if (!me || !isAdmin) return null

  const close = () => setDialog(null)
  const runtime = accounts.data?.runtime
  const polledFor = (account: AccountDTO) => (poller.data?.items ?? []).filter((entry) => entry.account_id === account.account_id)

  const columns: DataTableColumn<AccountDTO>[] = [
    {
      key: 'account',
      header: 'Account',
      width: 'minmax(150px, 1.2fr)',
      render: (account) => (
        <>
          <div className={styles.accountName}>{account.name}</div>
          <div className={`${styles.mono} ${styles.muted} ${styles.small}`}>{account.account_id}</div>
        </>
      ),
    },
    {
      key: 'role',
      header: 'Role Harbormaster assumes',
      width: 'minmax(220px, 2fr)',
      render: (account) => <span className={`${styles.mono} ${styles.small}`}>{account.role_arn}</span>,
    },
    { key: 'regions', header: 'Regions scanned', width: 'minmax(150px, 1fr)', render: (account) => account.regions.join(', ') },
    { key: 'clusters', header: 'Clusters', width: '80px', align: 'right', render: (account) => account.clusters },
    {
      key: 'status',
      header: 'Status',
      width: 'minmax(200px, 1.5fr)',
      render: (account) => <AccountStatus account={account} polled={polledFor(account)} tested={testResults[account.id]} />,
    },
    {
      key: 'last',
      header: 'Last check',
      width: '100px',
      render: (account) => <span className={styles.muted}>{account.last_ok_at ? <Ago ts={account.last_ok_at} /> : '–'}</span>,
    },
    {
      key: 'actions',
      header: '',
      width: '250px',
      align: 'right',
      render: (account) => (
        <span className={styles.rowActions}>
          <button
            type="button"
            className={`${styles.btn} ${styles.sm}`}
            disabled={testNow.isPending}
            onClick={() => testNow.mutate(account)}
          >
            Test now
          </button>
          <button type="button" className={`${styles.btn} ${styles.sm}`} onClick={() => setDialog({ kind: 'edit', account })}>
            Edit
          </button>
          <button type="button" className={`${styles.btn} ${styles.sm} ${styles.danger}`} onClick={() => setDialog({ kind: 'disconnect', account })}>
            Disconnect
          </button>
        </span>
      ),
    },
  ]

  return (
    <>
      <HeaderAction>
        <button type="button" className={`${styles.btn} ${styles.primary}`} onClick={() => setDialog({ kind: 'connect' })}>
          Connect account
        </button>
      </HeaderAction>

      <div className={styles.stack}>
        {accounts.data && (
          <Panel title="AWS accounts" flush actions={<span>Reached by assuming an IAM role</span>}>
            <DataTable
              label="AWS accounts"
              columns={columns}
              rows={accounts.data.items}
              rowKey={(account) => String(account.id)}
              minWidth={1100}
              empty="No accounts connected."
              rowStatus={(account) => {
                const polled = polledFor(account)
                if (account.status === 'error' || polled.some((entry) => entry.failing)) return 'bad'
                return polled.some((entry) => entry.throttled) ? 'warn' : undefined
              }}
            />
          </Panel>
        )}

        {runtime && (
          <div className={styles.grid}>
            <Panel title="Where Harbormaster runs">
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
            </Panel>
            {tags.data && (
              <Panel title="Tags">
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
                    <Link to="/settings/tags" className={`${styles.btn} ${styles.sm}`}>
                      Manage tags
                    </Link>
                  </div>
                </div>
              </Panel>
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
