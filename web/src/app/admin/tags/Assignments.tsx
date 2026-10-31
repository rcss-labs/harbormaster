import { useMutation, useQueryClient } from '@tanstack/react-query'
import { Fragment } from 'react'
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

/** One row per cluster, with its services indented beneath. Changing a select saves straight away. */
export function Assignments({ clusters, tags }: { clusters: ClusterAssignment[]; tags: TagItemDTO[] }) {
  const queryClient = useQueryClient()
  const toast = useToast()

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
    save.mutate({ kind, id, name, tagId: assignedTagId(kind, value, tags), shown: chosenTag(value, tags, clusterTag) })

  return (
    <Card title="Assignments" right="Dashed tags are inherited from the cluster">
      <Table>
        <thead>
          <tr>
            <th>Cluster / service</th>
            <th>Account · region</th>
            <th>Tag</th>
            <th>In effect</th>
          </tr>
        </thead>
        <tbody>
          {clusters.map((cluster) => (
            <Fragment key={cluster.id}>
              <tr className={styles.clusterRow}>
                <td className={tableClass.name}>{cluster.name}</td>
                <td className={styles.muted}>
                  {cluster.account_name} · {cluster.region}
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
              {cluster.services.map((service) => (
                <tr key={service.id}>
                  <td className={styles.indent}>{service.name}</td>
                  <td />
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
          ))}
        </tbody>
      </Table>
    </Card>
  )
}
