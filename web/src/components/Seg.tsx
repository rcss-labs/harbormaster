import type { ReactNode } from 'react'
import styles from './Seg.module.css'

interface SegProps<T extends string> {
  options: { value: T; label: ReactNode }[]
  value: T
  onChange: (value: T) => void
  label?: string
}

export function Seg<T extends string>({ options, value, onChange, label }: SegProps<T>) {
  return (
    <div className={styles.seg} role="group" aria-label={label}>
      {options.map((option) => (
        <button
          key={option.value}
          type="button"
          className={option.value === value ? styles.on : ''}
          aria-pressed={option.value === value}
          onClick={() => onChange(option.value)}
        >
          {option.label}
        </button>
      ))}
    </div>
  )
}
