import { useNetworks } from '../../api/networks'
import { EmptyState } from '../../components/EmptyState'
import { useCrumbs } from '../shell/crumbs'
import { countLabel, NetCard } from './NetCard'
import styles from './networks.module.css'

export function NetworksPage() {
  useCrumbs([['Networks']])
  const networks = useNetworks()
  const accounts = networks.data?.items ?? []

  return (
    <>
      <div className={styles.pageHead}>
        <div className={styles.titles}>
          <h1>Networks</h1>
          <div className={styles.meta}>Account level: regions with active VPCs. A VPC is active when ECS tasks run in it.</div>
        </div>
      </div>

      {networks.isError ? (
        <EmptyState>{networks.error instanceof Error ? networks.error.message : 'The networks could not be loaded.'}</EmptyState>
      ) : (
        accounts.map((account) => (
          <section key={account.account_id} className={styles.accountSection}>
            <h2 className={styles.accountHeading}>
              <span>{account.account_name}</span> <span className={`${styles.mono} ${styles.muted}`}>{account.account_id}</span>
            </h2>
            <div className={styles.netCards}>
              {account.regions.map((region) => {
                if (region.active_vpcs === 0) {
                  return <NetCard key={region.region} title={region.region} stats={[]} note="No active VPCs" off />
                }
                const notes = []
                if (region.hidden_vpcs > 0) {
                  notes.push(<div key="hidden">{countLabel(region.hidden_vpcs, 'VPC without tasks hidden', 'VPCs without tasks hidden')}</div>)
                }
                if (region.unplaced_tasks > 0) {
                  notes.push(<div key="unplaced">{`${region.unplaced_tasks} tasks without a subnet not shown`}</div>)
                }
                return (
                  <NetCard
                    key={region.region}
                    title={region.region}
                    stats={[
                      { value: region.active_vpcs, label: region.active_vpcs === 1 ? 'active VPC' : 'active VPCs' },
                      { value: region.clusters, label: region.clusters === 1 ? 'cluster' : 'clusters' },
                      { value: region.tasks, label: region.tasks === 1 ? 'task' : 'tasks' },
                    ]}
                    note={notes.length > 0 ? notes : undefined}
                    to={`/networks/${account.account_id}/${region.region}`}
                  />
                )
              })}
            </div>
          </section>
        ))
      )}
    </>
  )
}
