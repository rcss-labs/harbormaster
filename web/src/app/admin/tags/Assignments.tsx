import { useMutation, useQueryClient } from '@tanstack/react-query'
import { Fragment, useMemo, useState } from 'react'
import type { TagAssignmentsDTO, TagDTO, TagItemDTO } from '../../../api/types'
import { Card } from '../../../components/Card'
import { Table, tableClass } from '../../../components/Table'
import { TagChip } from '../../../components/TagChip'
import { useToast } from '../../../components/Toast'
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

interface RegionGroup {
  region: string
  clusters: ClusterAssignment[]
}
interface AccountGroup {
  name: string
  regions: RegionGroup[]
}

/** Groups clusters by account, then region, keeping the server's order of first appearance. */
function groupClusters(clusters: ClusterAssignment[]): AccountGroup[] {
  const accounts: AccountGroup[] = []
  for (const cluster of clusters) {
    let account = accounts.find((group) => group.name === cluster.account_name)
    if (!account) accounts.push((account = { name: cluster.account_name, regions: [] }))
    let region = account.regions.find((group) => group.region === cluster.region)
    if (!region) account.regions.push((region = { region: cluster.region, clusters: [] }))
    region.clusters.push(cluster)
  }
  return accounts
}

const plural = (count: number, noun: string) => `${count} ${noun}${count === 1 ? '' : 's'}`

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

// Expansion keys use names and ids, not positions, so they survive refetches that reorder or change rows.
const accountKey = (account: string) => `a:${account}`
const regionKey = (account: string, region: string) => `r:${account}|${region}`
const clusterKey = (clusterId: string) => `c:${clusterId}`

function Disclosure({ label, expanded, onToggle }: { label: string; expanded: boolean; onToggle: () => void }) {
  return (
    <button type="button" className={styles.disclosure} aria-expanded={expanded} onClick={onToggle}>
      <span className={styles.caret} aria-hidden="true">
        {expanded ? '▾' : '▸'}
      </span>
      {label}
    </button>
  )
}

/** Account → region → cluster → services, all collapsed at first. Changing a select saves straight away. */
export function Assignments({ clusters, tags }: { clusters: ClusterAssignment[]; tags: TagItemDTO[] }) {
  const queryClient = useQueryClient()
  const toast = useToast()
  const [expanded, setExpanded] = useState<ReadonlySet<string>>(new Set())
  const accounts = useMemo(() => groupClusters(clusters), [clusters])

  const toggle = (key: string) =>
    setExpanded((current) => {
      const next = new Set(current)
      if (!next.delete(key)) next.add(key)
      return next
    })
  const expandAll = () =>
    setExpanded(
      new Set(
        accounts.flatMap((account) => [
          accountKey(account.name),
          ...account.regions.flatMap((region) => [
            regionKey(account.name, region.region),
            ...region.clusters.map((cluster) => clusterKey(cluster.id)),
          ]),
        ]),
      ),
    )

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
    <Card
      title="Assignments"
      right={
        <>
          Dashed tags are inherited from the cluster
          <span className={styles.treeControls}>
            <button type="button" className={`${styles.btn} ${styles.sm}`} onClick={expandAll}>
              Expand all
            </button>
            <button type="button" className={`${styles.btn} ${styles.sm}`} onClick={() => setExpanded(new Set())}>
              Collapse all
            </button>
          </span>
        </>
      }
    >
      <Table>
        <thead>
          <tr>
            <th>Account / region / cluster / service</th>
            <th>Tag</th>
            <th>In effect</th>
          </tr>
        </thead>
        <tbody>
          {accounts.map((account) => {
            const accountClusters = account.regions.flatMap((region) => region.clusters)
            const accountOpen = expanded.has(accountKey(account.name))
            return (
              <Fragment key={account.name}>
                <tr className={styles.accountRow}>
                  <td className={tableClass.name}>
                    <Disclosure label={account.name} expanded={accountOpen} onToggle={() => toggle(accountKey(account.name))} />
                    <span className={`${styles.muted} ${styles.summary}`}>{summarize(accountClusters)}</span>
                  </td>
                  <td>
                    <span className={styles.chips}>
                      {distinctTags(accountClusters).map((tag) => (
                        <TagChip key={tag.id} tag={tag} />
                      ))}
                    </span>
                  </td>
                  <td />
                </tr>
                {accountOpen &&
                  account.regions.map((region) => {
                    const regionOpen = expanded.has(regionKey(account.name, region.region))
                    return (
                      <Fragment key={region.region}>
                        <tr className={styles.regionRow}>
                          <td className={`${styles.indentRegion} ${tableClass.name}`}>
                            <Disclosure
                              label={region.region}
                              expanded={regionOpen}
                              onToggle={() => toggle(regionKey(account.name, region.region))}
                            />
                            <span className={`${styles.muted} ${styles.summary}`}>{summarize(region.clusters)}</span>
                          </td>
                          <td>
                            <span className={styles.chips}>
                              {distinctTags(region.clusters).map((tag) => (
                                <TagChip key={tag.id} tag={tag} />
                              ))}
                            </span>
                          </td>
                          <td />
                        </tr>
                        {regionOpen &&
                          region.clusters.map((cluster) => {
                            const clusterOpen = expanded.has(clusterKey(cluster.id))
                            return (
                              <Fragment key={cluster.id}>
                                <tr className={styles.clusterRow}>
                                  <td className={`${styles.indentCluster} ${tableClass.name}`}>
                                    <Disclosure
                                      label={cluster.name}
                                      expanded={clusterOpen}
                                      onToggle={() => toggle(clusterKey(cluster.id))}
                                    />
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
                                    <TagChip
                                      tag={{
                                        ...cluster.effective,
                                        inherited: false,
                                      }}
                                      kind="cluster"
                                    />
                                  </td>
                                </tr>
                                {clusterOpen &&
                                  cluster.services.map((service) => (
                                    <tr key={service.id}>
                                      <td className={styles.indentService}>{service.name}</td>
                                      <td>
                                        <select
                                          className={styles.ctl}
                                          aria-label={`Tag for ${service.name}`}
                                          value={service.tag_id === null ? '' : String(service.tag_id)}
                                          onChange={(event) =>
                                            choose('service', service.id, service.name, event.target.value, cluster.effective)
                                          }
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
                                        <TagChip
                                          tag={{
                                            ...service.effective,
                                            inherited: service.tag_id === null,
                                          }}
                                        />
                                      </td>
                                    </tr>
                                  ))}
                              </Fragment>
                            )
                          })}
                      </Fragment>
                    )
                  })}
              </Fragment>
            )
          })}
        </tbody>
      </Table>
    </Card>
  )
}
