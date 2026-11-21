import { Fragment, useState } from 'react'
import { Link } from 'react-router'
import type { ConfigSearchItemDTO } from '../../api/configSearch'
import { Pill } from '../../components/Pill'
import { TagChip } from '../../components/TagChip'
import { MatchChips } from './MatchChips'
import { ServiceDetails } from './ServiceDetails'
import styles from './configsearch.module.css'

export interface ResultsProps {
  items: ConfigSearchItemDTO[]
  selected: ReadonlySet<string>
  onToggle: (id: string) => void
}

/** `payments-api:1.18.2` from a full image reference. */
export const shortImage = (image: string) => image.split('/').pop() ?? image

export const previousLabel = (item: ConfigSearchItemDTO) => `Previous revision :${item.matched_revision} is still running`

/** Desktop results: one row per service, with an expandable list of containers. */
export function ResultsTable({ items, selected, onToggle, onToggleAll }: ResultsProps & { onToggleAll: () => void }) {
  const [open, setOpen] = useState<ReadonlySet<string>>(new Set())
  const toggleOpen = (id: string) =>
    setOpen((current) => {
      const next = new Set(current)
      if (!next.delete(id)) next.add(id)
      return next
    })
  const allSelected = items.length > 0 && items.every((item) => selected.has(item.id))

  return (
    <div className={styles.tableWrap}>
      <table className={styles.table} aria-label="Config search results">
        <thead>
          <tr>
            <th className={styles.check}>
              <input type="checkbox" aria-label="Select all rows" checked={allSelected} onChange={onToggleAll} />
            </th>
            <th>Service</th>
            <th>Cluster</th>
            <th>Account and region</th>
            <th>Tag</th>
            <th>Revision</th>
            <th>Image</th>
            <th>Matched</th>
          </tr>
        </thead>
        <tbody>
          {items.map((item) => {
            const expanded = open.has(item.id)
            const first = item.containers[0]
            return (
              <Fragment key={item.id}>
                <tr>
                  <td className={styles.check}>
                    <input type="checkbox" aria-label={`Select ${item.name}`} checked={selected.has(item.id)} onChange={() => onToggle(item.id)} />
                  </td>
                  <td className={styles.service}>
                    <button
                      type="button"
                      className={styles.caret}
                      aria-expanded={expanded}
                      aria-label={`Details for ${item.name}`}
                      onClick={() => toggleOpen(item.id)}
                    >
                      {expanded ? '▾' : '▸'}
                    </button>
                    <Link to={`/services/${item.id}/task-definition`}>{item.name}</Link>
                  </td>
                  <td>{item.cluster.name}</td>
                  <td>
                    {item.account_name || item.account_id} · {item.region}
                  </td>
                  <td>
                    <TagChip tag={{ ...item.tag, inherited: item.tag_inherited }} />
                  </td>
                  <td>
                    <span className={styles.mono}>:{item.revision}</span>{' '}
                    {item.previous && <Pill kind="warn">{previousLabel(item)}</Pill>}
                  </td>
                  <td className={styles.mono}>
                    {first ? shortImage(first.image) : '–'}
                    {item.containers.length > 1 && <span className={styles.more}> +{item.containers.length - 1}</span>}
                  </td>
                  <td>
                    <MatchChips matches={item.matches} firstContainer={first?.name ?? ''} />
                  </td>
                </tr>
                {expanded && (
                  <tr className={styles.detailRow}>
                    <td />
                    <td colSpan={7}>
                      <ServiceDetails item={item} />
                    </td>
                  </tr>
                )}
              </Fragment>
            )
          })}
        </tbody>
      </table>
    </div>
  )
}
