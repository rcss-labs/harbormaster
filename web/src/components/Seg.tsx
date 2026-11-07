import type { KeyboardEvent, ReactNode } from 'react'
import styles from './Seg.module.css'
import { usePhone } from './usePhone'

interface SegOption<T extends string> {
  value: T
  label: ReactNode
  /** Shown after the label in a muted mono face. */
  count?: number
}

interface SegProps<T extends string> {
  options: SegOption<T>[]
  value: T
  onChange: (value: T) => void
  label?: string
  /** `toggle`: group of aria-pressed buttons (default). `radio`: single-choice filter with radiogroup semantics. */
  semantics?: 'toggle' | 'radio'
  /** Marks every control aria-disabled without disabling it, so it stays focusable and clicks can still explain why. */
  disabled?: boolean
  /** On phones the group fills the row; `fit` keeps it at its content width (Tail, Level). */
  fit?: boolean
}

export function Seg<T extends string>({ options, value, onChange, label, semantics = 'toggle', disabled, fit = false }: SegProps<T>) {
  const phone = usePhone()
  const radio = semantics === 'radio'

  const onKeyDown = (event: KeyboardEvent<HTMLButtonElement>, index: number) => {
    const step = event.key === 'ArrowRight' || event.key === 'ArrowDown' ? 1 : event.key === 'ArrowLeft' || event.key === 'ArrowUp' ? -1 : 0
    if (!step) return
    event.preventDefault()
    const next = options[(index + step + options.length) % options.length]
    onChange(next.value)
    const buttons = event.currentTarget.parentElement?.querySelectorAll('button')
    buttons?.[options.indexOf(next)]?.focus()
  }

  return (
    <div className={`${styles.seg} ${phone ? styles.phone : ''} ${fit ? styles.fit : ''}`.replace(/\s+/g, ' ').trim()} role={radio ? 'radiogroup' : 'group'} aria-label={label}>
      {options.map((option, index) => {
        const on = option.value === value
        return (
          <button
            key={option.value}
            type="button"
            data-hm-oneline=""
            className={on ? styles.on : ''}
            {...(radio ? { role: 'radio', 'aria-checked': on, tabIndex: on ? 0 : -1, onKeyDown: (e: KeyboardEvent<HTMLButtonElement>) => onKeyDown(e, index) } : { 'aria-pressed': on })}
            aria-disabled={disabled || undefined}
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
        )
      })}
    </div>
  )
}

/** Single-choice filter (All / Problems / ...) with optional counts and radiogroup semantics. */
export function SegmentedFilter<T extends string>(props: Omit<SegProps<T>, 'semantics'>) {
  return <Seg {...props} semantics="radio" />
}
