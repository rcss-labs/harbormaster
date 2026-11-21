import { useState } from 'react'
import { Link } from 'react-router'
import { Disclosure } from '../../components/Disclosure'
import { Pill } from '../../components/Pill'
import { TagChip } from '../../components/TagChip'
import { MatchChips } from './MatchChips'
import { previousLabel, shortImage, type ResultsProps } from './ResultsTable'
import { ServiceDetails } from './ServiceDetails'
import styles from './configsearch.module.css'

/** Phone results: one card per service. */
export function ResultCards({ items, selected, onToggle }: ResultsProps) {
  const [open, setOpen] = useState<ReadonlySet<string>>(new Set())
  const toggleOpen = (id: string) =>
    setOpen((current) => {
      const next = new Set(current)
      if (!next.delete(id)) next.add(id)
      return next
    })

  return (
    <ul className={styles.cards} aria-label="Matching services" data-layout="cards">
      {items.map((item) => {
        const first = item.containers[0]
        return (
          <li key={item.id} className={styles.card}>
            <div className={styles.cardHead}>
              <input type="checkbox" aria-label={`Select ${item.name}`} checked={selected.has(item.id)} onChange={() => onToggle(item.id)} />
              <Link to={`/services/${item.id}/task-definition`} className={styles.cardName}>
                {item.name}
              </Link>
              <TagChip tag={{ ...item.tag, inherited: item.tag_inherited }} />
            </div>
            <div className={styles.cardMeta}>
              <span>{item.cluster.name}</span> · <span>{item.region}</span>
            </div>
            <div className={styles.mono}>
              {first ? shortImage(first.image) : '–'}
              {item.containers.length > 1 && <span className={styles.more}> +{item.containers.length - 1}</span>}
            </div>
            <MatchChips matches={item.matches} firstContainer={first?.name ?? ''} />
            {item.previous && <Pill kind="warn">{previousLabel(item)}</Pill>}
            <Disclosure label="Details" expanded={open.has(item.id)} onToggle={() => toggleOpen(item.id)} />
            {open.has(item.id) && <ServiceDetails item={item} />}
          </li>
        )
      })}
    </ul>
  )
}
