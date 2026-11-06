import { Fragment, useEffect, useRef, useState, type ReactNode } from 'react'
import { Link } from 'react-router'
import { BottomSheet, SheetAction } from './BottomSheet'
import { Icon } from './Icon'
import styles from './Tabs.module.css'
import { usePhone } from './useNarrow'

export interface TabItem {
  key: string
  label: string
  /** Set when the user lacks the permission: shows a lock and explains why. */
  locked?: { action: string; tag: string }
  badge?: ReactNode
  /** Renders the label in <strong>. */
  bold?: boolean
  /** Items that share a group sit together after a gap; the group name labels the first one. Optional: tabs without it are unchanged. */
  group?: string
}

interface TabsProps {
  items: TabItem[]
  active: string
  hrefFor: (key: string) => string
  /** Accessible name of the tab bar. */
  ariaLabel?: string
}

export function Tabs({ items, active, hrefFor, ariaLabel }: TabsProps) {
  const phone = usePhone()
  const [allOpen, setAllOpen] = useState(false)
  const stripRef = useRef<HTMLElement>(null)
  const activeRef = useRef<HTMLAnchorElement>(null)
  // The strip scrolls sideways when it is too narrow: bring the open tab to the middle so it is never the clipped one.
  // Items arriving later (a page that loads its tabs after the data) re-run it too.
  useEffect(() => {
    const strip = stripRef.current
    const current = activeRef.current
    if (strip && current) strip.scrollLeft = Math.max(0, current.offsetLeft - (strip.clientWidth - current.offsetWidth) / 2)
  }, [active, items.length])
  const strip = (
    <nav ref={stripRef} className={styles.tabs} aria-label={ariaLabel}>
      {items.map((item, index) => {
        const previousGroup = index > 0 ? items[index - 1].group : undefined
        const groupChanged = index > 0 && item.group !== previousGroup
        const className = [item.key === active ? styles.active : '', item.locked ? styles.locked : ''].join(' ').trim()
        return (
          <Fragment key={item.key}>
            {groupChanged && <span role="separator" className={styles.gap} />}
            {groupChanged && item.group && <span className={styles.groupLabel}>{item.group}</span>}
          <Link
            ref={item.key === active ? activeRef : undefined}
            to={hrefFor(item.key)}
            className={className}
            title={item.locked ? `You need ${item.locked.action} on ${item.locked.tag}` : undefined}
          >
            {item.bold ? <strong>{item.label}</strong> : item.label}
            {item.locked && ' ⊘'}
            {item.badge !== undefined && <span className={styles.badge}>{item.badge}</span>}
          </Link>
          </Fragment>
        )
      })}
    </nav>
  )
  if (!phone) return strip
  return (
    <div className={styles.phoneWrap}>
      {strip}
      <button type="button" className={styles.all} aria-label="All sections" aria-haspopup="menu" aria-expanded={allOpen} onClick={() => setAllOpen(true)}>
        <Icon name="chevd" size={18} />
      </button>
      <BottomSheet open={allOpen} onClose={() => setAllOpen(false)} label="All sections" title="All sections" role="menu">
        {items.map((item) => (
          <SheetAction
            key={item.key}
            label={item.label}
            href={hrefFor(item.key)}
            current={item.key === active}
            disabled={item.locked !== undefined}
            why={item.locked ? `You need ${item.locked.action} on ${item.locked.tag}` : undefined}
            right={item.badge}
            onClick={() => setAllOpen(false)}
          />
        ))}
      </BottomSheet>
    </div>
  )
}
