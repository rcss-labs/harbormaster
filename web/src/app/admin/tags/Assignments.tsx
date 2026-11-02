import { useMutation, useQueryClient } from '@tanstack/react-query'
import { Fragment, useState } from 'react'
import type { TagAssignmentsDTO, TagDTO, TagItemDTO } from '../../../api/types'
import { Disclosure } from '../../../components/Disclosure'
import { Pager } from '../../../components/Pager'
import { Panel } from '../../../components/Panel'
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

export const ASSIGNMENTS_PAGE_SIZE = 15

const plural = (count: number, noun: string) => `${count} ${noun}${count === 1 ? '' : 's'}`

/** Clusters 15 to a page, each expandable to its services. Changing a select saves straight away. */
export function Assignments({ clusters, tags }: { clusters: ClusterAssignment[]; tags: TagItemDTO[] }) {
  const queryClient = useQueryClient()
  const toast = useToast()
  // Open rows are keyed by cluster id so they survive refetches and page changes.
  const [expanded, setExpanded] = useState<ReadonlySet<string>>(new Set())
  const [requestedPage, setPage] = useState(1)

  const pageCount = Math.max(1, Math.ceil(clusters.length / ASSIGNMENTS_PAGE_SIZE))
  const page = Math.min(requestedPage, pageCount)
  const visible = clusters.slice((page - 1) * ASSIGNMENTS_PAGE_SIZE, page * ASSIGNMENTS_PAGE_SIZE)

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
            <button type="button" className={`${styles.btn} ${styles.sm}`} onClick={() => setExpanded(new Set(clusters.map((cluster) => cluster.id)))}>
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
            <th>Cluster / service</th>
            <th>Tag</th>
            <th>In effect</th>
          </tr>
        </thead>
        <tbody>
          {visible.map((cluster) => {
            const open = expanded.has(cluster.id)
            return (
              <Fragment key={cluster.id}>
                <tr className={styles.clusterRow}>
                  <td className={tableClass.name}>
                    <Disclosure label={cluster.name} expanded={open} onToggle={() => toggle(cluster.id)} />
                    <span className={`${styles.muted} ${styles.summary}`}>
                      {`${cluster.account_name} · ${cluster.region} · ${plural(cluster.services.length, 'service')}`}
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
      <Pager page={page} pageSize={ASSIGNMENTS_PAGE_SIZE} total={clusters.length} onPageChange={setPage} />
    </Panel>
  )
}
