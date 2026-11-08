import { useMutation, useQueryClient } from '@tanstack/react-query'
import { Fragment, useMemo, useState } from 'react'
import type { TagAssignmentsDTO, TagDTO, TagItemDTO } from '../../../api/types'
import { Disclosure } from '../../../components/Disclosure'
import { Pager } from '../../../components/Pager'
import { Panel } from '../../../components/Panel'
import { Table, tableClass } from '../../../components/Table'
import { TagChip } from '../../../components/TagChip'
import { useToast } from '../../../components/Toast'
import { usePhone } from '../../../components/usePhone'
import styles from './tags.module.css'
import { type AssignmentKind, assignedTagId, chosenTag, invalidateTagQueries, saveAssignment } from './tagApi'

type ClusterAssignment = TagAssignmentsDTO['clusters'][number]

interface AssignmentChange {
  kind: AssignmentKind
  id: string
  name: string
  tagId: number | null
  /** What the toast shows after the change; undefined when there is no tag to show. */
  shown: TagDTO | undefined
}

export const ASSIGNMENTS_PAGE_SIZE = 15

const plural = (count: number, noun: string) => `${count} ${noun}${count === 1 ? '' : 's'}`

/** Clusters ordered account, then region (each in the server's order of first appearance), so groups are contiguous. */
function orderByGroup(clusters: ClusterAssignment[]): ClusterAssignment[] {
  const accounts = new Map<string, Map<string, ClusterAssignment[]>>()
  for (const cluster of clusters) {
    const regions = accounts.get(cluster.account_name) ?? new Map<string, ClusterAssignment[]>()
    accounts.set(cluster.account_name, regions)
    regions.set(cluster.region, [...(regions.get(cluster.region) ?? []), cluster])
  }
  return [...accounts.values()].flatMap((regions) => [...regions.values()].flat())
}

function summarize(clusters: ClusterAssignment[]): string {
  const services = clusters.reduce((total, cluster) => total + cluster.services.length, 0)
  return `${plural(clusters.length, 'cluster')} · ${plural(services, 'service')}`
}

/** Distinct effective tags (clusters and services) in a group, in first-seen order. */
function distinctTags(clusters: ClusterAssignment[]): TagDTO[] {
  const seen = new Map<number, TagDTO>()
  for (const cluster of clusters) {
    for (const tag of [cluster.effective, ...cluster.services.map((service) => service.effective)]) {
      if (!seen.has(tag.id)) seen.set(tag.id, { ...tag, inherited: false })
    }
  }
  return [...seen.values()]
}

const groupKey = (account: string, region?: string) => (region === undefined ? account : `${account}|${region}`)

/** Clusters 15 to a page under account and region headers, each cluster expandable to its services. Changing a select saves straight away. */
export function Assignments({ clusters, tags }: { clusters: ClusterAssignment[]; tags: TagItemDTO[] }) {
  const queryClient = useQueryClient()
  const toast = useToast()
  const phone = usePhone()
  // Open rows are keyed by cluster id so they survive refetches and page changes.
  const [expanded, setExpanded] = useState<ReadonlySet<string>>(new Set())
  const [requestedPage, setPage] = useState(1)

  const ordered = useMemo(() => orderByGroup(clusters), [clusters])
  // Group summaries cover the whole account/region, not just the part on the current page.
  const groups = useMemo(() => {
    const byKey = new Map<string, ClusterAssignment[]>()
    for (const cluster of ordered) {
      for (const key of [groupKey(cluster.account_name), groupKey(cluster.account_name, cluster.region)]) {
        byKey.set(key, [...(byKey.get(key) ?? []), cluster])
      }
    }
    return byKey
  }, [ordered])

  const pageCount = Math.max(1, Math.ceil(ordered.length / ASSIGNMENTS_PAGE_SIZE))
  const page = Math.min(requestedPage, pageCount)
  const visible = ordered.slice((page - 1) * ASSIGNMENTS_PAGE_SIZE, page * ASSIGNMENTS_PAGE_SIZE)

  const toggle = (id: string) =>
    setExpanded((current) => {
      const next = new Set(current)
      if (!next.delete(id)) next.add(id)
      return next
    })

  const save = useMutation({
    mutationFn: (change: AssignmentChange) => saveAssignment(change.kind, change.id, change.tagId),
    onSuccess: (_result, change) => {
      invalidateTagQueries(queryClient)
      toast.show(
        <>
          {change.name} is now {change.shown && <TagChip tag={change.shown} />}
        </>,
        'ok',
      )
    },
    onError: (caught) => toast.show(caught.message, 'bad'),
  })

  const choose = (kind: AssignmentKind, id: string, name: string, value: string, clusterTag?: TagDTO) =>
    save.mutate({
      kind,
      id,
      name,
      tagId: assignedTagId(kind, value, tags),
      shown: chosenTag(value, tags, clusterTag),
    })

  return (
    <Panel
      title="Assignments"
      flush
      actions={
        <>
          <span>Dashed tags are inherited from the cluster</span>
          <span className={styles.treeControls}>
            <button type="button" className={`${styles.btn} ${styles.sm}`} onClick={() => setExpanded(new Set(ordered.map((cluster) => cluster.id)))}>
              Expand all
            </button>
            <button type="button" className={`${styles.btn} ${styles.sm}`} onClick={() => setExpanded(new Set())}>
              Collapse all
            </button>
          </span>
        </>
      }
    >
      {phone ? (
        <div className={styles.assignList} role="list" aria-label="Assignments">
          {visible.map((cluster, index) => {
            const open = expanded.has(cluster.id)
            const previous = visible[index - 1]
            const newAccount = previous?.account_name !== cluster.account_name
            const newRegion = newAccount || previous?.region !== cluster.region
            return (
              <Fragment key={cluster.id}>
                {newAccount && <PhoneGroup label={cluster.account_name} members={groups.get(groupKey(cluster.account_name)) ?? []} />}
                {newRegion && <PhoneGroup label={cluster.region} members={groups.get(groupKey(cluster.account_name, cluster.region)) ?? []} />}
                <div role="listitem" className={styles.assignRow}>
                  <div className={styles.assignHead}>
                    <Disclosure label={cluster.name} expanded={open} onToggle={() => toggle(cluster.id)} />
                    <span className={`${styles.muted} ${styles.small}`}>{plural(cluster.services.length, 'service')}</span>
                  </div>
                  <div className={styles.assignTag}>
                    <select
                      className={styles.ctl}
                      aria-label={`Tag for ${cluster.name}`}
                      value={String(cluster.effective.id)}
                      onChange={(event) => choose('cluster', cluster.id, cluster.name, event.target.value)}
                    >
                      {tags.map((tag) => (
                        <option key={tag.id} value={tag.id}>
                          {tag.name}
                        </option>
                      ))}
                    </select>
                    <TagChip tag={{ ...cluster.effective, inherited: false }} kind="cluster" />
                  </div>
                </div>
                {open &&
                  cluster.services.map((service) => (
                    <div role="listitem" key={service.id} className={`${styles.assignRow} ${styles.assignService}`}>
                      <div className={styles.serviceName}>{service.name}</div>
                      <div className={styles.assignTag}>
                        <select
                          className={styles.ctl}
                          aria-label={`Tag for ${service.name}`}
                          value={service.tag_id === null ? '' : String(service.tag_id)}
                          onChange={(event) => choose('service', service.id, service.name, event.target.value, cluster.effective)}
                        >
                          <option value="">{`Inherit (${cluster.effective.name})`}</option>
                          {tags.map((tag) => (
                            <option key={tag.id} value={tag.id}>
                              {tag.name}
                            </option>
                          ))}
                        </select>
                        <TagChip tag={{ ...service.effective, inherited: service.tag_id === null }} />
                      </div>
                    </div>
                  ))}
              </Fragment>
            )
          })}
        </div>
      ) : (
      <Table>
        <thead>
          <tr>
            <th>Account / region / cluster / service</th>
            <th>Tag</th>
            <th>In effect</th>
          </tr>
        </thead>
        <tbody>
          {visible.map((cluster, index) => {
            const open = expanded.has(cluster.id)
            const previous = visible[index - 1]
            const newAccount = previous?.account_name !== cluster.account_name
            const newRegion = newAccount || previous?.region !== cluster.region
            return (
              <Fragment key={cluster.id}>
                {newAccount && <GroupRow level="account" label={cluster.account_name} members={groups.get(groupKey(cluster.account_name)) ?? []} />}
                {newRegion && (
                  <GroupRow
                    level="region"
                    label={cluster.region}
                    members={groups.get(groupKey(cluster.account_name, cluster.region)) ?? []}
                  />
                )}
                <tr className={styles.clusterRow}>
                  <td className={tableClass.name}>
                    <Disclosure label={cluster.name} expanded={open} onToggle={() => toggle(cluster.id)} />
                    <span className={`${styles.muted} ${styles.summary}`}>
                      {plural(cluster.services.length, 'service')}
                    </span>
                  </td>
                  <td>
                    <select
                      className={styles.ctl}
                      aria-label={`Tag for ${cluster.name}`}
                      value={String(cluster.effective.id)}
                      onChange={(event) => choose('cluster', cluster.id, cluster.name, event.target.value)}
                    >
                      {tags.map((tag) => (
                        <option key={tag.id} value={tag.id}>
                          {tag.name}
                        </option>
                      ))}
                    </select>
                  </td>
                  <td>
                    <TagChip tag={{ ...cluster.effective, inherited: false }} kind="cluster" />
                  </td>
                </tr>
                {open &&
                  cluster.services.map((service) => (
                    <tr key={service.id}>
                      <td className={styles.indentService}>{service.name}</td>
                      <td>
                        <select
                          className={styles.ctl}
                          aria-label={`Tag for ${service.name}`}
                          value={service.tag_id === null ? '' : String(service.tag_id)}
                          onChange={(event) => choose('service', service.id, service.name, event.target.value, cluster.effective)}
                        >
                          <option value="">{`Inherit (${cluster.effective.name})`}</option>
                          {tags.map((tag) => (
                            <option key={tag.id} value={tag.id}>
                              {tag.name}
                            </option>
                          ))}
                        </select>
                      </td>
                      <td>
                        <TagChip tag={{ ...service.effective, inherited: service.tag_id === null }} />
                      </td>
                    </tr>
                  ))}
              </Fragment>
            )
          })}
        </tbody>
      </Table>
      )}
      <Pager page={page} pageSize={ASSIGNMENTS_PAGE_SIZE} total={ordered.length} onPageChange={setPage} />
    </Panel>
  )
}

/** Phone version of the group header: name and totals, then the distinct tags. */
function PhoneGroup({ label, members }: { label: string; members: ClusterAssignment[] }) {
  return (
    <div role="listitem" className={styles.assignGroup}>
      <div>
        <span className={styles.groupName}>{label}</span>
        <div className={`${styles.muted} ${styles.small}`}>{summarize(members)}</div>
      </div>
      <span className={styles.chips}>
        {distinctTags(members).map((tag) => (
          <TagChip key={tag.id} tag={tag} />
        ))}
      </span>
    </div>
  )
}

/** Non-interactive account or region header above its clusters, with the group's totals and distinct tags. */
function GroupRow({ level, label, members }: { level: 'account' | 'region'; label: string; members: ClusterAssignment[] }) {
  return (
    <tr className={`${styles.groupRow} ${level === 'region' ? styles.regionRow : ''}`.trim()}>
      <td>
        <span className={styles.groupName}>{label}</span>
        <span className={`${styles.muted} ${styles.summary}`}>{summarize(members)}</span>
      </td>
      <td>
        <span className={styles.chips}>
          {distinctTags(members).map((tag) => (
            <TagChip key={tag.id} tag={tag} />
          ))}
        </span>
      </td>
      <td />
    </tr>
  )
}
