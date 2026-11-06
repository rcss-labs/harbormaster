import { useCallback } from 'react'
import { Navigate, useParams, useSearchParams } from 'react-router'
import { DataBanner } from '../../components/DataBanner'
import { Drawer } from '../../components/Drawer'
import { EmptyState } from '../../components/EmptyState'
import { usePhone } from '../../components/useNarrow'
import { useCrumbs } from '../shell/crumbs'
import styles from './networks.module.css'
import { useVpcList, type VpcEntry } from './useVpcList'
import { VpcDetail } from './VpcDetail'

const matches = (vpc: VpcEntry, needle: string) =>
  `${vpc.name} ${vpc.vpcId} ${vpc.cidr} ${vpc.accountName} ${vpc.region}`.toLowerCase().includes(needle)

/** Groups consecutive VPCs by account; the list is already ordered by account. */
function groupByAccount(vpcs: VpcEntry[]) {
  const groups: { accountId: string; accountName: string; vpcs: VpcEntry[] }[] = []
  for (const vpc of vpcs) {
    const last = groups[groups.length - 1]
    if (last && last.accountId === vpc.accountId) last.vpcs.push(vpc)
    else groups.push({ accountId: vpc.accountId, accountName: vpc.accountName, vpcs: [vpc] })
  }
  return groups
}

export function NetworksPage() {
  useCrumbs([['Networks']])
  const [searchParams, setSearchParams] = useSearchParams()
  const phone = usePhone()
  const { vpcs, accountCount, hiddenVpcs, isLoading, error } = useVpcList()
  const q = searchParams.get('q') ?? ''
  const selectedKey = searchParams.get('vpc')

  const setParam = useCallback(
    (name: string, value: string) =>
      setSearchParams(
        (current) => {
          const next = new URLSearchParams(current)
          if (value) next.set(name, value)
          else next.delete(name)
          return next
        },
        { replace: true },
      ),
    [setSearchParams],
  )

  const needle = q.trim().toLowerCase()
  const visible = needle ? vpcs.filter((vpc) => matches(vpc, needle)) : vpcs
  // A selection hidden by the filter stays open; with none chosen the first listed VPC opens.
  // A chosen key that is not in the list (gone, or not visible to this user) never opens another VPC.
  const chosen = selectedKey ? vpcs.find((vpc) => vpc.key === selectedKey) : undefined
  const selected = selectedKey ? chosen : (visible[0] ?? vpcs[0])
  const unknownSelection = Boolean(selectedKey) && !chosen && !isLoading
  const subnetTotal = vpcs.reduce((sum, vpc) => sum + vpc.subnets, 0)
  const taskTotal = vpcs.reduce((sum, vpc) => sum + vpc.tasks, 0)
  // On phones the detail is a sheet that opens for a chosen VPC only; the first VPC is not opened on load.
  const sheetVpc = phone && chosen ? chosen : undefined

  return (
    <div className={styles.page}>
      <div className={styles.pageHead}>
        <h1 className="hm-phone-title-sr">Networks</h1>
        {vpcs.length > 0 && (
          <span className={styles.headMeta}>
            {`${vpcs.length} ${vpcs.length === 1 ? 'VPC' : 'VPCs'} · ${accountCount} ${accountCount === 1 ? 'account' : 'accounts'} · ${subnetTotal} subnets · ${taskTotal} running tasks`}
          </span>
        )}
      </div>

      <DataBanner tiers={['Networks']} />

      {error && vpcs.length === 0 ? (
        <EmptyState>{error instanceof Error ? error.message : 'The networks could not be loaded.'}</EmptyState>
      ) : !isLoading && vpcs.length === 0 ? (
        <EmptyState>No active VPCs. A VPC is active when ECS tasks run in it.</EmptyState>
      ) : (
        <div className={`hm-fill ${styles.layout}`}>
          <section className={styles.listPanel} aria-label="VPCs">
            <div className={styles.listSearch}>
              <input
                type="search"
                aria-label="Filter VPCs"
                placeholder="Filter VPCs, accounts, regions"
                value={q}
                onChange={(event) => setParam('q', event.target.value)}
              />
            </div>
            <div className={styles.listScroll}>
              {visible.length === 0 && !isLoading && <div className={styles.listEmpty}>No VPCs match this filter.</div>}
              {groupByAccount(visible).map((group) => (
                <div key={group.accountId}>
                  <div className={styles.groupHead}>
                    <span>{group.accountName}</span>
                    <span className={styles.groupCount}>{group.vpcs.length}</span>
                  </div>
                  {group.vpcs.map((vpc) => {
                    const on = vpc.key === selected?.key
                    return (
                      <button
                        key={vpc.key}
                        type="button"
                        className={`${styles.vpcRow} ${on ? styles.vpcOn : ''}`.trim()}
                        aria-pressed={on}
                        onClick={() => setParam('vpc', vpc.key)}
                      >
                        <span className={styles.vpcStripe} aria-hidden="true" />
                        <span className={styles.vpcText}>
                          <span className={styles.vpcName}>{vpc.name}</span>
                          <span className={styles.sub}>{`${vpc.region} · ${vpc.cidr}`}</span>
                          {phone && <span className={styles.sub}>{`${vpc.vpcId} · ${vpc.accountName}`}</span>}
                        </span>
                        <span className={styles.vpcCounts}>{`${vpc.clusterCount} cl · ${vpc.tasks} t`}</span>
                      </button>
                    )
                  })}
                </div>
              ))}
            </div>
            {hiddenVpcs > 0 && (
              <div className={styles.listFoot}>{`${hiddenVpcs} ${hiddenVpcs === 1 ? 'VPC' : 'VPCs'} without tasks hidden`}</div>
            )}
          </section>
          {sheetVpc ? (
            <Drawer
              open
              onClose={() => setParam('vpc', '')}
              title={sheetVpc.name}
              sub={`${sheetVpc.vpcId} · ${sheetVpc.cidr} · ${sheetVpc.accountName} · ${sheetVpc.region}`}
            >
              <VpcDetail key={sheetVpc.key} entry={sheetVpc} headerless />
            </Drawer>
          ) : phone ? (
            unknownSelection && <EmptyState>No running tasks you can see in this VPC.</EmptyState>
          ) : (
            <div className={styles.detail}>
              {selected && <VpcDetail key={selected.key} entry={selected} />}
              {unknownSelection && <EmptyState>No running tasks you can see in this VPC.</EmptyState>}
            </div>
          )}
        </div>
      )}
    </div>
  )
}

/** Old `/networks/:account/:region/:vpc` links open the VPC in the new layout. */
export function NetworkVpcRedirect() {
  const { accountId = '', region = '', vpcId = '' } = useParams()
  return <Navigate replace to={`/networks?${new URLSearchParams({ vpc: `${accountId}/${region}/${vpcId}` })}`} />
}

/** Old `/networks/:account/:region` links open the list filtered to that region. */
export function NetworkRegionRedirect() {
  const { region = '' } = useParams()
  return <Navigate replace to={`/networks?${new URLSearchParams({ q: region })}`} />
}
