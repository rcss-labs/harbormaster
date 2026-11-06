import type { ReactNode } from 'react'
import styles from './Chips.module.css'

export interface ChipOption<T extends string> {
  value: T
  label: ReactNode
  count?: number
}

interface ChipsProps<T extends string> {
  /** Accessible name of the group. */
  label: string
  options: ChipOption<T>[]
  value: T
  onChange: (value: T) => void
  /** Wrap onto more rows instead of scrolling sideways; for short sets. */
  wrap?: boolean
}

/**
 * Single-choice filter chips (saved views, task state, status counts). On phones the row scrolls sideways with a fade
 * while there is more to see; `wrap` lays a short set out on as many rows as it needs.
 */
export function Chips<T extends string>({ label, options, value, onChange, wrap = false }: ChipsProps<T>) {
  return (
    <div role="group" aria-label={label} className={`hm-chips ${styles.chips} ${wrap ? 'wrap' : ''}`.trim()}>
      {options.map((option) => (
        <button
          key={option.value}
          type="button"
          className={styles.chip}
          aria-pressed={option.value === value}
          data-hm-hit="36"
          data-hm-oneline=""
          onClick={() => onChange(option.value)}
        >
          {option.label}
          {option.count !== undefined && (
            <>
              {' '}
              <span className={styles.count}>{option.count}</span>
            </>
          )}
        </button>
      ))}
    </div>
  )
}
