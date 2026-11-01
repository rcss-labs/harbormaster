import { Link } from 'react-router'
import { nodeHref, type TreeNode } from '../../../api/networks'
import type { TagColor, TagDTO } from '../../../api/types'
import { Pill } from '../../../components/Pill'
import { statusKind, statusLabel } from '../../../components/status'
import { TagChip } from '../../../components/TagChip'
import styles from './tree.module.css'

const KIND_LABEL = {
  vpc: 'VPC',
  az: 'Availability zone',
  nosubnet: 'No subnet',
  cluster: 'Cluster',
  service: 'Service',
  standalone: 'Standalone tasks',
} as const

const plural = (n: number, noun: string) => `${n} ${noun}${n === 1 ? '' : 's'}`

/** Percentage of used IPs rounded to 2 decimals, at least 2 so a nearly empty subnet still shows a sliver. */
export function ipBarPercent(used: number, total: number): number {
  const percent = Math.round((used / total) * 10_000) / 100
  return Math.min(100, Math.max(2, percent))
}

/** Number of `.ns` detail lines each node kind renders; they get ids `${base}-d0`, `-d1`, … for aria-describedby. */
const DETAIL_LINES: Record<TreeNode['kind'], number> = { vpc: 2, az: 1, nosubnet: 1, subnet: 2, cluster: 1, service: 1, standalone: 1, task: 0 }

export function describedByIds(node: TreeNode, base: string): string[] {
  return Array.from({ length: DETAIL_LINES[node.kind] }, (_, index) => `${base}-d${index}`)
}

interface NodeBodyProps {
  node: TreeNode
  /** Unique id prefix of the owning treeitem; see describedByIds. */
  base: string
  closed: boolean
  onToggle(key: string): void
}

export function NodeBody({ node, base, closed, onToggle }: NodeBodyProps) {
  const href = nodeHref(node)
  const name = (
    <span id={`${base}-n`}>
      {href ? (
        <Link to={href} tabIndex={-1}>
          {node.label}
        </Link>
      ) : (
        node.label
      )}
    </span>
  )
  const toggle = (node.children?.length ?? 0) > 0 && (
    <button
      type="button"
      className={styles.tog}
      tabIndex={-1}
      aria-label={closed ? 'Expand' : 'Collapse'}
      onClick={() => onToggle(node.key)}
    >
      {closed ? '+' : '−'}
    </button>
  )

  switch (node.kind) {
    case 'task': {
      // The Go API sends "" for a standalone task's missing service and for tasks without a group.
      const { task } = node
      return (
        <Link
          to={href ?? '/tasks'}
          tabIndex={-1}
          className={`${styles.node} ${styles.task} ${task.unhealthy ? styles.unhealthy : ''}`}
          id={`${base}-n`}
          title={`Task ${task.task_id}`}
        >
          {node.label}
          <span className={styles.ip}>{task.private_ip}</span>
          {task.group ? <span className={styles.group}>{task.group}</span> : null}
        </Link>
      )
    }
    case 'vpc':
      return (
        <div className={`${styles.node} ${styles.vpc}`}>
          <span id={`${base}-k`} className={styles.kind}>{KIND_LABEL.vpc}</span>
          <div className={styles.nh}>
            {name}
            {toggle}
          </div>
          <span id={`${base}-d0`} className={`${styles.ns} ${styles.mono}`}>
            {node.vpc.vpc_id} · {node.vpc.cidr}
          </span>
          <span id={`${base}-d1`} className={styles.ns}>
            {plural(node.vpc.zones, 'zone')} · {plural(node.tasks, 'task')}
          </span>
        </div>
      )
    case 'az':
      return (
        <div className={`${styles.node} ${styles.az}`}>
          <span id={`${base}-k`} className={styles.kind}>{KIND_LABEL.az}</span>
          <div className={styles.nh}>
            {name}
            {toggle}
          </div>
          <span id={`${base}-d0`} className={styles.ns}>
            {plural(node.az.subnets, 'subnet')} · {plural(node.tasks, 'task')}
          </span>
        </div>
      )
    case 'nosubnet':
      return (
        <div className={`${styles.node} ${styles.nosubnet}`}>
          <span id={`${base}-k`} className={styles.kind}>{KIND_LABEL.nosubnet}</span>
          <div className={styles.nh}>
            {name}
            {toggle}
          </div>
          <span id={`${base}-d0`} className={styles.ns}>{plural(node.tasks, 'task')}</span>
        </div>
      )
    case 'subnet': {
      const { subnet } = node
      return (
        <div className={`${styles.node} ${styles.subnet} ${subnet.public ? styles.public : styles.private}`}>
          <span id={`${base}-k`} className={styles.kind}>{subnet.public ? 'Public subnet' : 'Private subnet'}</span>
          <div className={styles.nh}>
            {name}
            {toggle}
          </div>
          <span id={`${base}-d0`} className={`${styles.ns} ${styles.mono}`}>
            {subnet.subnet_id} · {subnet.cidr}
          </span>
          <span id={`${base}-d1`} className={styles.ns}>
            {subnet.ips_used} of {subnet.ips_total} IPs used
          </span>
          {/* With no usable addresses (total 0) there is nothing to measure, so no bar is drawn. */}
          {subnet.ips_total > 0 && (
            <div className={styles.ipbar}>
              <span style={{ width: `${ipBarPercent(subnet.ips_used, subnet.ips_total)}%` }} />
            </div>
          )}
        </div>
      )
    }
    case 'cluster': {
      const { tag } = node.cluster
      // An unknown cluster tag arrives as {name:"",color:""}; TagChip needs a TagDTO, so adapt the two known fields.
      const tagDto: TagDTO = { id: 0, name: tag.name, color: tag.color as TagColor, inherited: false }
      return (
        <div className={`${styles.node} ${styles.cluster}`}>
          <span id={`${base}-k`} className={styles.kind}>{KIND_LABEL.cluster}</span>
          <div className={styles.nh}>
            {name}
            {tag.name !== '' && <TagChip tag={tagDto} kind="cluster" />}
            {toggle}
          </div>
          <span id={`${base}-d0`} className={styles.ns}>
            {plural(node.cluster.services, 'service')} · {plural(node.tasks, 'task')}
          </span>
        </div>
      )
    }
    case 'service':
      return (
        <div className={`${styles.node} ${styles.service}`}>
          <span id={`${base}-k`} className={styles.kind}>{KIND_LABEL.service}</span>
          <div className={styles.nh}>
            {name}
            <Pill kind={statusKind(node.service.status)}>{statusLabel(node.service.status)}</Pill>
            {toggle}
          </div>
          <span id={`${base}-d0`} className={styles.ns}>
            {plural(node.tasks, 'task')} here · :{node.service.revision}
          </span>
        </div>
      )
    case 'standalone':
      return (
        <div className={`${styles.node} ${styles.standalone}`}>
          <div className={styles.nh}>
            <span id={`${base}-k`} className={styles.kind}>{KIND_LABEL.standalone}</span>
            {toggle}
          </div>
          <span id={`${base}-d0`} className={styles.ns}>{plural(node.tasks, 'task')}</span>
        </div>
      )
  }
}
