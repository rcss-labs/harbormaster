import { Link, useParams } from 'react-router'
import { ApiError } from '../../api/client'
import { useTreeState } from './tree/useTreeState'
import { Tree } from './tree/Tree'
import { useVpcTree, type TreeEdge } from '../../api/networks'
import { Card } from '../../components/Card'
import { EmptyState } from '../../components/EmptyState'
import { useCrumbs } from '../shell/crumbs'
import { pluralize } from './NetCard'
import styles from './networks.module.css'

/** Edge summary segments in the page header: one per gateway kind, plus one line listing the endpoint services. */
function edgeSummary(edges: TreeEdge[]): string[] {
  const parts: string[] = []
  if (edges.some((edge) => edge.kind === 'igw')) parts.push('Internet gateway')
  const natCount = edges.filter((edge) => edge.kind === 'nat').length
  if (natCount === 1) parts.push('NAT gateway')
  else if (natCount > 1) parts.push(`${natCount} NAT gateways`)
  const endpoints = edges
    .filter((edge) => edge.kind === 'endpoint' && edge.detail)
    .map((edge) => edge.detail as string)
    .sort()
  if (endpoints.length > 0) parts.push(`Endpoints: ${endpoints.join(', ')}`)
  return parts
}

export function NetworkVpcPage() {
  const { accountId = '', region = '', vpcId = '' } = useParams()
  const treeQuery = useVpcTree(accountId, region, vpcId)
  const data = treeQuery.data
  const treeState = useTreeState(data?.root, data?.summary.collapsed_by_default ?? false, vpcId)
  const vpcName = data?.vpc.name ?? vpcId
  // The tree response carries the account name, so no separate account lookup is needed here.
  useCrumbs([['Networks', '/networks'], [data?.vpc.account_name ?? accountId, '/networks'], [region, `/networks/${accountId}/${region}`], [vpcName]])

  const notFound = treeQuery.error instanceof ApiError && treeQuery.error.status === 404
  const summary = data?.summary
  const edges = edgeSummary(data?.edges ?? [])

  return (
    <>
      <div className={styles.pageHead}>
        <div className={styles.titles}>
          <h1>{vpcName}</h1>
          {data && summary && (
            <div className={styles.meta}>
              <span className={styles.mono}>{data.vpc.vpc_id}</span>
              <span>{data.vpc.cidr}</span>
              <span>{`${summary.subnets} ${pluralize(summary.subnets, 'subnet', 'subnets')}`}</span>
              <span>{`${summary.running_tasks} running tasks`}</span>
              {edges.map((edge) => (
                <span key={edge}>{edge}</span>
              ))}
              {summary.unplaced_tasks > 0 && <span>{`${summary.unplaced_tasks} tasks without a subnet not shown`}</span>}
            </div>
          )}
        </div>
        {data && (
          <div className={styles.actions}>
            <button type="button" className={styles.btn} onClick={treeState.expandAll}>
              Expand all
            </button>
            <button type="button" className={styles.btn} onClick={treeState.collapseToServices}>
              Collapse to services
            </button>
          </div>
        )}
      </div>

      {notFound ? (
        <EmptyState>
          No running tasks you can see in this VPC. <Link to={`/networks/${accountId}/${region}`}>Back to {region}</Link>
        </EmptyState>
      ) : treeQuery.isError ? (
        <EmptyState>{treeQuery.error instanceof Error ? treeQuery.error.message : 'The network tree could not be loaded.'}</EmptyState>
      ) : data ? (
        <Card
          title="Network tree"
          right={<span className={styles.muted}>VPC ▸ availability zone ▸ subnet ▸ cluster ▸ service ▸ task</span>}
        >
          <Tree root={data.root} closed={treeState.closed} onToggle={treeState.toggle} ariaLabel="Network tree" />
        </Card>
      ) : null}
    </>
  )
}
