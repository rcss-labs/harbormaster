import { useNetworks } from '../../api/networks'
import { EmptyState } from '../../components/EmptyState'
import { useCrumbs } from '../shell/crumbs'
import { countLabel, NetCard, pluralize } from './NetCard'
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
                const unplacedNote =
                  region.unplaced_tasks > 0 ? (
                    <div>{countLabel(region.unplaced_tasks, 'task without a subnet not shown', 'tasks without a subnet not shown')}</div>
                  ) : null

                if (region.active_vpcs === 0) {
                  return (
                    <NetCard
                      key={region.region}
                      title={region.region}
                      stats={[]}
                      note={
                        <>
                          <div>No active VPCs</div>
                          {unplacedNote}
                        </>
                      }
                      off
                    />
                  )
                }

                const hiddenNote =
                  region.hidden_vpcs > 0 ? (
                    <div>{countLabel(region.hidden_vpcs, 'VPC without tasks hidden', 'VPCs without tasks hidden')}</div>
                  ) : null
                return (
                  <NetCard
                    key={region.region}
                    title={region.region}
                    stats={[
                      { value: region.active_vpcs, label: pluralize(region.active_vpcs, 'active VPC', 'active VPCs') },
                      { value: region.clusters, label: pluralize(region.clusters, 'cluster', 'clusters') },
                      { value: region.tasks, label: pluralize(region.tasks, 'task', 'tasks') },
                    ]}
                    note={
                      hiddenNote || unplacedNote ? (
                        <>
                          {hiddenNote}
                          {unplacedNote}
                        </>
                      ) : undefined
                    }
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
