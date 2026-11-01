import { useEffect, useRef, useState } from 'react'
import styles from './MultiSelect.module.css'

export interface MultiSelectOption {
  value: string
  label: string
}

interface MultiSelectProps {
  label: string
  placeholder: string
  options: MultiSelectOption[]
  value: string[]
  onChange: (values: string[]) => void
}

/** A button that opens a checkbox list; the button text is the placeholder or the selected labels. */
export function MultiSelect({ label, placeholder, options, value, onChange }: MultiSelectProps) {
  const [open, setOpen] = useState(false)
  const wrap = useRef<HTMLDivElement>(null)

  useEffect(() => {
    if (!open) return
    const onDocumentClick = (event: MouseEvent) => {
      if (!wrap.current?.contains(event.target as Node)) setOpen(false)
    }
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') setOpen(false)
    }
    document.addEventListener('click', onDocumentClick)
    document.addEventListener('keydown', onKeyDown)
    return () => {
      document.removeEventListener('click', onDocumentClick)
      document.removeEventListener('keydown', onKeyDown)
    }
  }, [open])

  const selectedLabels = options.filter((o) => value.includes(o.value)).map((o) => o.label)
  const toggle = (optionValue: string) =>
    onChange(value.includes(optionValue) ? value.filter((v) => v !== optionValue) : [...value, optionValue])

  return (
    <div className={styles.wrap} ref={wrap}>
      <button
        type="button"
        className={styles.button}
        aria-haspopup="true"
        aria-expanded={open}
        aria-label={label}
        onClick={() => setOpen((current) => !current)}
      >
        <span className={styles.text}>{selectedLabels.length > 0 ? selectedLabels.join(', ') : placeholder}</span>
      </button>
      {open && (
        <div className={styles.popover} role="group" aria-label={`${label} options`}>
          {options.map((option) => (
            <label key={option.value} className={styles.option}>
              <input type="checkbox" checked={value.includes(option.value)} onChange={() => toggle(option.value)} />
              {option.label}
            </label>
          ))}
        </div>
      )}
    </div>
  )
}
