import { useEffect, useRef, useState } from 'react'
import styles from './MultiSelect.module.css'

export interface MultiSelectOption {
  value: string
  label: string
  /** Secondary text after the label, such as a count. */
  hint?: string
  /** Tag colour name; draws a swatch before the label. */
  swatch?: string
  /** Section heading; options with the same group sit together, in order of first appearance. */
  group?: string
  /** Extra text the search matches besides the label and hint. */
  keywords?: string
}

interface MultiSelectProps {
  label: string
  placeholder: string
  options: MultiSelectOption[]
  value: string[]
  onChange: (values: string[]) => void
  /** Adds a search box that filters the options by label, hint and keywords. */
  searchable?: boolean
  /** With `searchable`: turns the search text into an option the list lacks, offered as an "Add" button. */
  creatable?: (query: string) => MultiSelectOption | null
  /** Shows the selection as removable chips under the button instead of as button text; the button is full width. */
  chips?: boolean
}

const matches = (option: MultiSelectOption, query: string) =>
  `${option.label} ${option.hint ?? ''} ${option.keywords ?? ''}`.toLowerCase().includes(query)

const swatchStyle = (color: string) => ({ background: `var(--tag-${color}-text, var(--muted))` })

/**
 * A button that opens a checkbox list. The button text is the placeholder or the selected labels; with `chips` the
 * selection is shown as removable chips. With `searchable` the list filters as you type.
 */
export function MultiSelect({ label, placeholder, options, value, onChange, searchable = false, creatable, chips = false }: MultiSelectProps) {
  const [open, setOpen] = useState(false)
  const [query, setQuery] = useState('')
  const wrap = useRef<HTMLDivElement>(null)
  const search = useRef<HTMLInputElement>(null)

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

  useEffect(() => {
    if (open) search.current?.focus()
  }, [open])

  const selectedOptions = value.map((v) => options.find((o) => o.value === v)).filter((o): o is MultiSelectOption => o !== undefined)
  const toggle = (optionValue: string) =>
    onChange(value.includes(optionValue) ? value.filter((v) => v !== optionValue) : [...value, optionValue])

  const needle = query.trim().toLowerCase()
  const visible = searchable && needle ? options.filter((o) => matches(o, needle)) : options
  const groups: { name: string | undefined; items: MultiSelectOption[] }[] = []
  for (const option of visible) {
    const bucket = groups.find((g) => g.name === option.group)
    if (bucket) bucket.items.push(option)
    else groups.push({ name: option.group, items: [option] })
  }
  const created = searchable && creatable && query.trim() ? creatable(query.trim()) : null
  const offerCreate = created !== null && !options.some((o) => o.value === created.value)

  return (
    <div className={`${styles.wrap} ${chips ? styles.block : ''}`.trim()} ref={wrap}>
      <button
        type="button"
        className={`${styles.button} ${chips ? styles.fullWidth : ''}`.trim()}
        aria-haspopup="true"
        aria-expanded={open}
        aria-label={label}
        onClick={() => {
          setQuery('')
          setOpen((current) => !current)
        }}
      >
        <span className={styles.text}>{!chips && selectedOptions.length > 0 ? selectedOptions.map((o) => o.label).join(', ') : placeholder}</span>
      </button>
      {open && (
        <div className={styles.popover} role="group" aria-label={`${label} options`}>
          {searchable && (
            <input
              ref={search}
              type="search"
              className={styles.search}
              aria-label={`Search ${label}`}
              placeholder="Search"
              autoComplete="off"
              spellCheck={false}
              value={query}
              onChange={(event) => setQuery(event.target.value)}
            />
          )}
          <div className={styles.list}>
            {groups.map((group) => (
              <div key={group.name ?? ''} className={styles.group}>
                {group.name && <div className={styles.groupName}>{group.name}</div>}
                {group.items.map((option) => (
                  <label key={option.value} className={styles.option}>
                    <input type="checkbox" checked={value.includes(option.value)} onChange={() => toggle(option.value)} />
                    {option.swatch && <span className={styles.swatch} aria-hidden style={swatchStyle(option.swatch)} />}
                    <span className={styles.optionLabel}>{option.label}</span>
                    {option.hint && <span className={styles.hint}>{option.hint}</span>}
                  </label>
                ))}
              </div>
            ))}
            {visible.length === 0 && !offerCreate && <p className={styles.none}>Nothing matches.</p>}
            {offerCreate && created && (
              <button
                type="button"
                className={styles.create}
                onClick={() => {
                  onChange([...value, created.value])
                  setQuery('')
                }}
              >
                {`Add ${created.label}`}
              </button>
            )}
          </div>
        </div>
      )}
      {chips && selectedOptions.length > 0 && (
        <ul className={styles.chips} aria-label={`Selected: ${label}`}>
          {selectedOptions.map((option) => (
            <li key={option.value} className={styles.chip}>
              {option.swatch && <span className={styles.swatch} aria-hidden style={swatchStyle(option.swatch)} />}
              <span className={styles.chipLabel}>{option.label}</span>
              <button type="button" className={styles.chipRemove} aria-label={`Remove ${option.label}`} onClick={() => toggle(option.value)}>
                ×
              </button>
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}
