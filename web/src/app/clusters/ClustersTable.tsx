import { Link } from 'react-router'
import type { ClusterCardDTO } from '../../api/types'
import { Sparkline } from '../../charts/Sparkline'
import { Pill } from '../../components/Pill'
import { Table, tableClass } from '../../components/Table'
import { TagChip } from '../../components/TagChip'
import { clusterStatusKind, clusterStatusLabel } from '../../components/status'
import { type ClusterMetricsItem, formatPercent, presentPoints } from './ClusterCardMetrics'
import styles from './clusters.module.css'

export function ClustersTable({
  clusters,
  metricsById,
}: {
  clusters: ClusterCardDTO[]
  metricsById: Record<string, ClusterMetricsItem>
}) {
  return (
    <div className={styles.panel}>
      <Table>
        <thead>
          <tr>
            <th>Cluster</th>
            <th>Label</th>
            <th>Status</th>
            <th>Account</th>
            <th>Region</th>
            <th className={tableClass.num}>Services</th>
            <th className={tableClass.num}>Tasks</th>
            <th>CPU, 24 h</th>
            <th className={tableClass.num}>CPU now</th>
            <th className={tableClass.num}>Memory now</th>
          </tr>
        </thead>
        <tbody>
          {clusters.map((cluster) => {
            const metrics = metricsById[cluster.id]
            const cpuPoints = presentPoints(metrics?.cpu ?? [])
            return (
            <tr key={cluster.id}>
              <td className={tableClass.name}>
                <Link to={`/clusters/${cluster.id}`}>{cluster.name}</Link>
              </td>
              <td>
                <TagChip tag={cluster.tag} kind="cluster" />
              </td>
              <td>
                <Pill kind={clusterStatusKind(cluster.status)}>{clusterStatusLabel(cluster.status)}</Pill>
              </td>
              <td>{cluster.account_name}</td>
              <td>{cluster.region}</td>
              <td className={tableClass.num}>{cluster.services.length}</td>
              <td className={tableClass.num}>
                {cluster.running} / {cluster.desired}
              </td>
              <td>
                {!cluster.container_insights ? (
                  <span className={styles.muted}>Container Insights off</span>
                ) : cpuPoints.length > 0 ? (
                  <Sparkline values={cpuPoints} color="var(--series-1)" height={26} />
                ) : (
                  '–'
                )}
              </td>
              <td className={tableClass.num}>{cluster.container_insights ? formatPercent(metrics?.cpu_now) : '–'}</td>
              <td className={tableClass.num}>{cluster.container_insights ? formatPercent(metrics?.mem_now) : '–'}</td>
            </tr>
            )
          })}
        </tbody>
      </Table>
    </div>
  )
}
