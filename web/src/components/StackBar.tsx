import styles from './StackBar.module.css'

export interface StackBarPart {
  n: number
  /** CSS colour, normally a token such as `var(--ok)`. */
  color: string
  label: string
}

interface StackBarProps {
  parts: StackBarPart[]
  /** Denominator for segment widths and the accessible name; defaults to the sum of the parts. */
  total?: number
  /** What the bar measures, e.g. "Target health"; shown as a tooltip and as the legend's name. */
  label: string
  /** List the parts with their counts under the bar. */
  legend?: boolean
}

/** Horizontal stacked bar that replaces the old donuts. The counts are in the accessible name, not only the colours. */
export function StackBar({ parts, total, label, legend = false }: StackBarProps) {
  const sum = parts.reduce((acc, part) => acc + part.n, 0)
  const denominator = total ?? sum
  const name = `${parts.map((part) => `${part.label} ${part.n}`).join(', ')} of ${denominator}`
  return (
    <div className={styles.wrap}>
      <div className={styles.bar} role="img" aria-label={name} title={label}>
        {parts
          .filter((part) => part.n > 0)
          .map((part) => (
            <i
              key={part.label}
              style={{ width: `${denominator > 0 ? (part.n / denominator) * 100 : 0}%`, minWidth: '2px', background: part.color }}
            />
          ))}
      </div>
      {legend && (
        <ul className={styles.legend} aria-label={label}>
          {parts.map((part) => (
            <li key={part.label}>
              <span className={styles.swatch} style={{ background: part.color }} aria-hidden="true" />
              {part.label} <span className={styles.count}>{part.n}</span>
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}
