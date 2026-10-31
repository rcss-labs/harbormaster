import { Link } from 'react-router'
import type { ClusterCardDTO } from '../../api/types'
import { Pill } from '../../components/Pill'
import { Table, tableClass } from '../../components/Table'
import { TagChip } from '../../components/TagChip'
import { clusterStatusKind, clusterStatusLabel } from '../../components/status'
import styles from './clusters.module.css'

export function ClustersTable({ clusters }: { clusters: ClusterCardDTO[] }) {
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
          </tr>
        </thead>
        <tbody>
          {clusters.map((cluster) => (
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
            </tr>
          ))}
        </tbody>
      </Table>
    </div>
  )
}
