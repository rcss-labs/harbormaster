import { useState } from 'react'
import { BottomSheet } from './BottomSheet'
import { Chips } from './Chips'
import type { SortState } from './DataTable'
import { Icon } from './Icon'
import { Seg } from './Seg'
import styles from './SortFieldsButton.module.css'

export interface SortFieldOption {
  key: string
  label: string
}

export interface FieldOption extends SortFieldOption {
  /** Always shown; its toggle is disabled. */
  locked?: boolean
}

interface SortFieldsButtonProps {
  /** Columns the list can be sorted by. */
  sortOptions: SortFieldOption[]
  sort: SortState | null
  onSortChange: (sort: SortState) => void
  /** Fields that can be switched on and off for each row. Omit for a sort-only sheet. */
  columns?: FieldOption[]
  /** Keys of the shown fields. */
  visible?: string[]
  onVisibleChange?: (keys: string[]) => void
  /** What "Reset to default" restores; every field and the current sort when omitted. */
  defaults?: { sort?: SortState | null; visible?: string[] }
}

/** Phone replacement for sortable column headers and the column picker: one button, one sheet. */
export function SortFieldsButton({ sortOptions, sort, onSortChange, columns, visible, onVisibleChange, defaults }: SortFieldsButtonProps) {
  const [open, setOpen] = useState(false)
  const direction = sort?.dir ?? 'asc'
  const shown = visible ?? columns?.map((column) => column.key) ?? []

  const toggleField = (key: string) => {
    if (!columns || !onVisibleChange) return
    const next = new Set(shown)
    if (next.has(key)) next.delete(key)
    else next.add(key)
    onVisibleChange(columns.filter((column) => column.locked || next.has(column.key)).map((column) => column.key))
  }

  const reset = () => {
    if (defaults?.sort) onSortChange(defaults.sort)
    if (columns && onVisibleChange) onVisibleChange(defaults?.visible ?? columns.map((column) => column.key))
  }

  return (
    <>
      <button type="button" className={styles.button} aria-label="Sort and fields" aria-haspopup="dialog" aria-expanded={open} onClick={() => setOpen(true)}>
        <Icon name="sliders" size={18} />
      </button>
      <BottomSheet
        open={open}
        onClose={() => setOpen(false)}
        label="Sort and fields"
        title="Sort and fields"
        footer={
          <>
            <button type="button" className={styles.action} onClick={reset}>
              Reset to default
            </button>
            <button type="button" className={`${styles.action} ${styles.primary}`} onClick={() => setOpen(false)}>
              Done
            </button>
          </>
        }
      >
        <div className={styles.stack}>
          <span className={styles.lab}>Sort by</span>
          <Chips
            wrap
            label="Sort by"
            options={sortOptions.map((option) => ({ value: option.key, label: option.label }))}
            value={sort?.key ?? ''}
            onChange={(key) => onSortChange({ key, dir: direction })}
          />
        </div>
        <Seg
          label="Direction"
          options={[
            { value: 'asc', label: 'Ascending' },
            { value: 'desc', label: 'Descending' },
          ]}
          value={direction}
          onChange={(dir) => onSortChange({ key: sort?.key ?? sortOptions[0]?.key ?? '', dir })}
        />
        {columns && onVisibleChange && (
          <div className={styles.stack}>
            <div className={styles.spread}>
              <span className={styles.lab}>Fields on each row</span>
              <span className={styles.count}>
                {shown.length} of {columns.length}
              </span>
            </div>
            <div role="group" aria-label="Fields on each row" className={styles.fields}>
              {columns.map((column) => {
                const on = column.locked || shown.includes(column.key)
                return (
                  <button key={column.key} type="button" className={styles.field} aria-pressed={on} disabled={column.locked} onClick={() => toggleField(column.key)}>
                    <i aria-hidden="true" />
                    {column.label}
                  </button>
                )
              })}
            </div>
          </div>
        )}
      </BottomSheet>
    </>
  )
}
