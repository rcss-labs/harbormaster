import { useParams } from 'react-router'
import { useNetworkRegion, useNetworks } from '../../api/networks'
import { EmptyState } from '../../components/EmptyState'
import { useCrumbs } from '../shell/crumbs'
import { NetCard } from './NetCard'
import styles from './networks.module.css'

export function NetworkRegionPage() {
  const { accountId = '', region = '' } = useParams()
  const networks = useNetworks()
  const regionVpcs = useNetworkRegion(accountId, region)
  // The region response has no account name, so take it from the account list; fall back to the id when the shell filter hides the account.
  const accountName = networks.data?.items.find((account) => account.account_id === accountId)?.account_name ?? accountId
  useCrumbs([['Networks', '/networks'], [accountName, '/networks'], [region]])
  const vpcs = regionVpcs.data?.items ?? []

  return (
    <>
      <div className={styles.pageHead}>
        <div className={styles.titles}>
          <h1>{region}</h1>
          <div className={styles.meta}>Active VPCs in {accountName}</div>
        </div>
      </div>

      {regionVpcs.isError ? (
        <EmptyState>{regionVpcs.error instanceof Error ? regionVpcs.error.message : 'The VPCs could not be loaded.'}</EmptyState>
      ) : regionVpcs.isLoading ? null : vpcs.length === 0 ? (
        <EmptyState>No active VPCs in this region.</EmptyState>
      ) : (
        <div className={styles.netCards}>
          {vpcs.map((vpc) => (
            <NetCard
              key={vpc.vpc_id}
              title={vpc.name}
              subtitle={`${vpc.vpc_id} · ${vpc.cidr}`}
              stats={[
                { value: vpc.subnets, label: vpc.subnets === 1 ? 'subnet' : 'subnets' },
                { value: vpc.clusters.length, label: vpc.clusters.length === 1 ? 'cluster' : 'clusters' },
                { value: vpc.tasks, label: vpc.tasks === 1 ? 'task' : 'tasks' },
              ]}
              note={vpc.clusters.join(', ')}
              to={`/networks/${accountId}/${region}/${vpc.vpc_id}`}
            />
          ))}
        </div>
      )}
    </>
  )
}
