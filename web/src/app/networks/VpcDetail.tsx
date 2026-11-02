import { useState } from 'react'
import { Link } from 'react-router'
import { ApiError } from '../../api/client'
import { useVpcTree, type TreeEdge, type TreeNode, type VpcTreeResponse } from '../../api/networks'
import { DataTable, type DataTableColumn } from '../../components/DataTable'
import { EmptyState } from '../../components/EmptyState'
import { KpiStrip } from '../../components/KpiStrip'
import { Panel } from '../../components/Panel'
import { STATUS_COLOR, statusKind } from '../../components/status'
import styles from './networks.module.css'
import { placementRows } from './placement'
import type { VpcEntry } from './useVpcList'

interface SubnetRow {
  id: string
  name: string
  isPublic: boolean
  zone: string
  cidr: string
  used: number
  total: number
  clusters: number
  tasks: number
}

function subnetRows(root: TreeNode): SubnetRow[] {
  const rows: SubnetRow[] = []
  for (const zone of root.children ?? []) {
    if (zone.kind !== 'az') continue
    for (const node of zone.children ?? []) {
      if (node.kind !== 'subnet') continue
      rows.push({
        id: node.subnet.subnet_id,
        name: node.label,
        isPublic: node.subnet.public,
        zone: zone.label,
        cidr: node.subnet.cidr,
        used: node.subnet.ips_used,
        total: node.subnet.ips_total,
        clusters: node.children?.filter((child) => child.kind === 'cluster').length ?? 0,
        tasks: node.tasks,
      })
    }
  }
  return rows
}

/** Above this share of a subnet's addresses the IP bar turns amber. */
const IP_WARNING_SHARE = 0.8

const subnetColumns: DataTableColumn<SubnetRow>[] = [
  {
    key: 'subnet',
    header: 'Subnet',
    width: 'minmax(0,1.2fr)',
    render: (row) => (
      <span className={styles.twoLine}>
        <span className={styles.strong}>{row.name}</span>
        <span className={styles.sub}>{row.id}</span>
      </span>
    ),
  },
  { key: 'type', header: 'Type', width: '70px', render: (row) => <span className={row.isPublic ? styles.publicType : undefined}>{row.isPublic ? 'Public' : 'Private'}</span> },
  { key: 'zone', header: 'Zone', width: '110px', render: (row) => <span className={styles.mono}>{row.zone}</span> },
  { key: 'cidr', header: 'CIDR', width: '130px', render: (row) => <span className={styles.mono}>{row.cidr}</span> },
  {
    key: 'ips',
    header: 'IPs used',
    width: 'minmax(0,1fr)',
    render: (row) => {
      const share = row.total > 0 ? row.used / row.total : 0
      return (
        <span className={styles.ipUse}>
          <span className={styles.track} aria-hidden="true">
            <span style={{ width: `${Math.max(1, share * 100)}%`, background: share > IP_WARNING_SHARE ? 'var(--warning)' : 'var(--accent)' }} />
          </span>
          <span className={styles.ipText}>{`${row.used} of ${row.total}`}</span>
        </span>
      )
    },
  },
  { key: 'clusters', header: 'Clusters', width: '64px', align: 'right', render: (row) => <span className={styles.mono}>{row.clusters}</span> },
  { key: 'tasks', header: 'Tasks', width: '56px', align: 'right', render: (row) => <span className={styles.mono}>{row.tasks}</span> },
]

function Gateways({ edges }: { edges: TreeEdge[] }) {
  const ids = (kind: string) => edges.filter((edge) => edge.kind === kind).map((edge) => edge.id)
  const endpoints = edges
    .filter((edge) => edge.kind === 'endpoint')
    .map((edge) => edge.detail || edge.id)
    .sort()
  const igw = ids('igw')
  const nat = ids('nat')
  const rows: [string, string][] = [
    ['Internet gateway', igw.length > 0 ? igw.join(', ') : 'none'],
    ['NAT gateways', nat.length > 0 ? nat.join(', ') : 'none'],
    ['VPC endpoints', endpoints.length > 0 ? endpoints.join(' · ') : 'none'],
  ]
  return (
    <Panel title="Gateways and endpoints">
      <dl className={styles.gateways}>
        {rows.map(([label, value]) => (
          <div key={label} className={styles.gatewayRow}>
            <dt>{label}</dt>
            <dd>{value}</dd>
          </div>
        ))}
      </dl>
    </Panel>
  )
}

function Placement({ data }: { data: VpcTreeResponse }) {
  // Clusters are closed until opened; "Expand all" flips the default and forgets individual choices.
  const [allOpen, setAllOpen] = useState(false)
  const [overrides, setOverrides] = useState<Record<string, boolean>>({})
  const isOpen = (key: string) => overrides[key] ?? allOpen
  const rows = placementRows(data.root, isOpen)
  return (
    <Panel
      title="Placement"
      flush
      toolbar={<span className={styles.sub}>zone ▸ subnet ▸ cluster ▸ service</span>}
      actions={
        <button
          type="button"
          className={styles.linkButton}
          onClick={() => {
            setAllOpen(!allOpen)
            setOverrides({})
          }}
        >
          {allOpen ? 'Collapse to clusters' : 'Expand all'}
        </button>
      }
    >
      <ul className={styles.outline}>
        {rows.map((row) => {
          const indent = { paddingLeft: 12 + row.depth * 18 }
          const label = (
            <>
              <span className={styles.chev} aria-hidden="true">
                {row.toggles ? (row.open ? '▾' : '▸') : ''}
              </span>
              <span className={styles.kind}>{row.kind}</span>
              <i className={styles.dot} style={{ background: row.status ? STATUS_COLOR[statusKind(row.status)] : 'transparent' }} aria-hidden="true" />
            </>
          )
          return (
            <li key={row.key} className={styles.outlineRow} style={indent}>
              {row.toggles ? (
                <button
                  type="button"
                  className={styles.outlineToggle}
                  aria-expanded={row.open}
                  onClick={() => setOverrides((current) => ({ ...current, [row.key]: !row.open }))}
                >
                  {label}
                  <span className={styles.strong}>{row.name}</span>
                  <span className={styles.meta}>{row.meta}</span>
                </button>
              ) : (
                <div className={styles.outlineStatic}>
                  {label}
                  {row.href ? (
                    <Link to={row.href} className={styles.strong}>
                      {row.name}
                    </Link>
                  ) : (
                    <span className={styles.strong}>{row.name}</span>
                  )}
                  <span className={styles.meta}>{row.meta}</span>
                </div>
              )}
              {row.toggles && row.href && (
                <Link to={row.href} className={styles.openLink} aria-label={`Open cluster ${row.name}`}>
                  ↗
                </Link>
              )}
            </li>
          )
        })}
      </ul>
    </Panel>
  )
}

const withSub = (value: string | number, sub: string) => (
  <>
    {value}
    <small className={styles.kpiSub}>{sub}</small>
  </>
)

/** The right-hand column of the Networks page: summary, subnets, placement outline, gateways. */
export function VpcDetail({ entry }: { entry: VpcEntry }) {
  const tree = useVpcTree(entry.accountId, entry.region, entry.vpcId)
  const data = tree.data
  const notFound = tree.error instanceof ApiError && tree.error.status === 404

  const header = (
    <div className={styles.vpcHead}>
      <h2>{entry.name}</h2>
      <span className={styles.mono}>
        {entry.vpcId} · {entry.cidr} · {entry.accountName} · {entry.region}
      </span>
    </div>
  )

  if (notFound) return <>{header}<EmptyState>No running tasks you can see in this VPC.</EmptyState></>
  if (tree.isError) {
    return <>{header}<EmptyState>{tree.error instanceof Error ? tree.error.message : 'The VPC could not be loaded.'}</EmptyState></>
  }
  if (!data) return header

  const subnets = subnetRows(data.root)
  const privateCount = subnets.filter((subnet) => !subnet.isPublic).length
  const usedIps = subnets.reduce((sum, subnet) => sum + subnet.used, 0)
  const totalIps = subnets.reduce((sum, subnet) => sum + subnet.total, 0)
  const clusterKeys = new Set<string>()
  const visit = (node: TreeNode) => {
    if (node.kind === 'cluster') clusterKeys.add(node.cluster.cluster_id)
    node.children?.forEach(visit)
  }
  visit(data.root)
  const natCount = data.edges.filter((edge) => edge.kind === 'nat').length

  return (
    <>
      {header}
      {data.summary.unplaced_tasks > 0 && (
        <div className={styles.sub}>{`${data.summary.unplaced_tasks} tasks without a subnet not shown`}</div>
      )}
      <KpiStrip
        label="VPC summary"
        items={[
          { label: 'Subnets', value: withSub(subnets.length, `${privateCount} private · ${subnets.length - privateCount} public`) },
          { label: 'Clusters', value: clusterKeys.size },
          { label: 'Running tasks', value: withSub(data.summary.running_tasks, `${data.summary.zones} ${data.summary.zones === 1 ? 'zone' : 'zones'}`) },
          { label: 'IPs used', value: withSub(usedIps, `of ${totalIps}`) },
          { label: 'NAT gateways', value: withSub(natCount, natCount > 1 ? 'one per zone' : natCount === 1 ? 'single zone' : 'none') },
        ]}
      />
      <Panel title="Subnets" flush>
        <DataTable label="Subnets" columns={subnetColumns} rows={subnets} rowKey={(row) => row.id} minWidth={720} maxHeight={360} />
      </Panel>
      <div className={styles.twoCol}>
        <Placement data={data} />
        <Gateways edges={data.edges} />
      </div>
    </>
  )
}
