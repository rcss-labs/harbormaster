import styles from './Disclosure.module.css'

interface DisclosureProps {
  label: string
  expanded: boolean
  onToggle: () => void
}

/** Caret button that opens and closes a group of rows; the label is its accessible name. */
export function Disclosure({ label, expanded, onToggle }: DisclosureProps) {
  return (
    <button type="button" className={styles.disclosure} aria-expanded={expanded} onClick={onToggle}>
      <span className={styles.caret} aria-hidden="true">
        {expanded ? '▾' : '▸'}
      </span>
      {label}
    </button>
  )
}
