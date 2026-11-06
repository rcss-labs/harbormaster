import type { TreeNode } from '../../api/networks'
import type { Status } from '../../api/types'
import { serviceSeverity } from '../clusters/serviceSeverity'

export type PlacementKind = 'Zone' | 'Subnet' | 'Cluster' | 'Service'

export interface PlacementRow {
  key: string
  depth: number
  kind: PlacementKind
  name: string
  meta: string
  /** Status of the worst service below (or the service itself); undefined for zones and subnets. */
  status?: Status
  /** Only cluster rows toggle. */
  toggles: boolean
  open: boolean
  href?: string
}

const plural = (count: number, noun: string) => `${count} ${noun}${count === 1 ? '' : 's'}`

function worstStatus(node: TreeNode): Status {
  let worst: Status = 'steady'
  const visit = (child: TreeNode) => {
    if (child.kind === 'service' && serviceSeverity(child.service.status) > serviceSeverity(worst)) worst = child.service.status
    child.children?.forEach(visit)
  }
  visit(node)
  return worst
}

/**
 * The visible rows of the placement outline: zone, subnet, cluster, service. Clusters are closed unless `isOpen` says
 * otherwise; individual tasks are not listed (the Tasks page has them).
 */
export function placementRows(root: TreeNode, isOpen: (clusterKey: string) => boolean): PlacementRow[] {
  const rows: PlacementRow[] = []
  const walk = (node: TreeNode, depth: number) => {
    switch (node.kind) {
      case 'az':
        rows.push({ key: node.key, depth, kind: 'Zone', name: node.label, meta: `${plural(node.az.subnets, 'subnet')} · ${plural(node.tasks, 'task')}`, toggles: false, open: true })
        break
      case 'nosubnet':
        rows.push({ key: node.key, depth, kind: 'Zone', name: 'No subnet', meta: plural(node.tasks, 'task'), toggles: false, open: true })
        break
      case 'subnet':
        rows.push({
          key: node.key,
          depth,
          kind: 'Subnet',
          name: node.label,
          meta: `${node.subnet.cidr} · ${node.subnet.ips_used} of ${node.subnet.ips_total} IPs${node.tasks === 0 ? ' · no running tasks' : ''}`,
          toggles: false,
          open: true,
        })
        break
      case 'cluster': {
        const open = isOpen(node.key)
        const services = node.children?.filter((child) => child.kind === 'service').length ?? 0
        rows.push({
          key: node.key,
          depth,
          kind: 'Cluster',
          name: node.label,
          meta: `${plural(services, 'service')} · ${plural(node.tasks, 'task')} here`,
          status: worstStatus(node),
          toggles: true,
          open,
          href: `/clusters/${node.cluster.cluster_id}`,
        })
        if (!open) return
        break
      }
      case 'service': {
        const unhealthy = node.children?.filter((child) => child.kind === 'task' && child.task.unhealthy).length ?? 0
        rows.push({
          key: node.key,
          depth,
          kind: 'Service',
          name: node.label,
          meta: `${plural(node.tasks, 'task')} here · :${node.service.revision}${unhealthy > 0 ? ` · ${unhealthy} unhealthy` : ''}`,
          status: node.service.status,
          toggles: false,
          open: true,
          href: `/services/${node.service.service_id}/overview`,
        })
        return
      }
      case 'standalone':
        rows.push({ key: node.key, depth, kind: 'Service', name: 'Standalone tasks', meta: plural(node.tasks, 'task'), toggles: false, open: true })
        return
      case 'task':
        return
      case 'vpc':
        depth -= 1
        break
    }
    node.children?.forEach((child) => walk(child, depth + 1))
  }
  walk(root, 0)
  return rows
}
