import { useCallback, useState } from 'react'
import { Icon } from './Icon'
import { Popover } from './Popover'
import styles from './ColumnPicker.module.css'

export interface ColumnPickerColumn {
  key: string
  label: string
  /** Always visible; its checkbox is disabled. */
  locked?: boolean
}

interface ColumnPickerProps {
  columns: ColumnPickerColumn[]
  /** Keys of the visible columns. */
  visible: string[]
  onChange: (keys: string[]) => void
  /** What "Reset to default" restores; every column when omitted. */
  defaults?: string[]
}

/** "Columns" button with a checklist popover. Reports visible keys in column order; locked columns stay on. */
export function ColumnPicker({ columns, visible, onChange, defaults }: ColumnPickerProps) {
  const [open, setOpen] = useState(false)
  const toggle = (key: string) => {
    const next = new Set(visible)
    if (next.has(key)) next.delete(key)
    else next.add(key)
    onChange(columns.filter((column) => column.locked || next.has(column.key)).map((column) => column.key))
  }
  return (
    <div className={styles.wrap}>
      <button type="button" className={styles.button} aria-haspopup="dialog" aria-expanded={open} onClick={() => setOpen((value) => !value)}>
        <Icon name="columns" />
        Columns
      </button>
      <Popover open={open} onClose={() => setOpen(false)} label="Choose columns" align="right" width={240}>
        <ul className={styles.list}>
          {columns.map((column) => (
            <li key={column.key}>
              <label className={styles.item}>
                <input
                  type="checkbox"
                  checked={column.locked || visible.includes(column.key)}
                  disabled={column.locked}
                  onChange={() => toggle(column.key)}
                />
                {column.label}
              </label>
            </li>
          ))}
        </ul>
        <button
          type="button"
          className={styles.reset}
          onClick={() => onChange(defaults ?? columns.map((column) => column.key))}
        >
          Reset to default
        </button>
      </Popover>
    </div>
  )
}

const readKeys = (storageKey: string): string[] | undefined => {
  try {
    const parsed: unknown = JSON.parse(localStorage.getItem(storageKey) ?? 'null')
    if (Array.isArray(parsed) && parsed.every((item) => typeof item === 'string')) return parsed
  } catch {
    // unreadable or unavailable storage: the caller falls back
  }
  return undefined
}

/**
 * Column visibility remembered in localStorage under `storageKey` (for example `harbormaster-columns:services`).
 *
 * `introduced` lists columns added after users may have saved their choice. A saved list cannot tell "never offered" from
 * "hidden on purpose", so each save also records which introduced columns existed then (under `<storageKey>:seen`); an
 * introduced column the saved choice has never seen is shown, and one the user saw and left out stays hidden.
 */
export function useColumnPrefs(storageKey: string, defaults: string[], introduced: string[] = []): [string[], (keys: string[]) => void] {
  const [keys, setKeys] = useState<string[]>(() => {
    const saved = readKeys(storageKey)
    if (!saved) return defaults
    const seen = readKeys(`${storageKey}:seen`) ?? []
    const unseen = introduced.filter((key) => !seen.includes(key) && !saved.includes(key))
    return [...saved, ...unseen]
  })
  const introducedKey = introduced.join(',')
  const update = useCallback(
    (next: string[]) => {
      setKeys(next)
      try {
        localStorage.setItem(storageKey, JSON.stringify(next))
        if (introducedKey) localStorage.setItem(`${storageKey}:seen`, JSON.stringify(introducedKey.split(',')))
      } catch {
        // storage unavailable: the choice lasts for this page view only
      }
    },
    [storageKey, introducedKey],
  )
  return [keys, update]
}
