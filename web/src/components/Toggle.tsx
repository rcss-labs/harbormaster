import type { ReactNode } from 'react'
import styles from './Toggle.module.css'

interface ToggleProps {
  checked: boolean
  onChange: (checked: boolean) => void
  children: ReactNode
  disabled?: boolean
}

/** Labelled checkbox used for view options such as "Group repeats". */
export function Toggle({ checked, onChange, children, disabled = false }: ToggleProps) {
  return (
    <label className={`${styles.toggle} ${disabled ? styles.disabled : ''}`.trim()}>
      <input type="checkbox" checked={checked} disabled={disabled} onChange={(event) => onChange(event.target.checked)} />
      <span>{children}</span>
    </label>
  )
}
