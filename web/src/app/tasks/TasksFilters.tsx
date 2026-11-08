import type { TaskDTO } from '../../api/types'
import { BottomSheet } from '../../components/BottomSheet'
import { Icon } from '../../components/Icon'
import { TASK_COLUMNS } from '../../components/TaskTable'
import { Toggle } from '../../components/Toggle'
import styles from './TasksFilters.module.css'

/** The sheet's own filters, applied on top of the loaded tasks. Each is a URL parameter of the same name. */
export const SHEET_FILTERS = [
  { key: 'service', label: 'Service' },
  { key: 'cluster', label: 'Cluster' },
  { key: 'status', label: 'Status' },
  { key: 'capacity', label: 'Capacity' },
  { key: 'cause', label: 'Stop cause' },
] as const

export type SheetFilterKey = (typeof SHEET_FILTERS)[number]['key']
export type SheetFilterValues = Record<SheetFilterKey, string>

const valueOf: Record<SheetFilterKey, (task: TaskDTO) => string> = {
  service: (task) => task.service?.name ?? '',
  cluster: (task) => task.cluster.name,
  status: (task) => task.status,
  capacity: (task) => task.capacity,
  cause: (task) => task.stop_cause,
}

export const matchesSheetFilters = (task: TaskDTO, values: SheetFilterValues) =>
  SHEET_FILTERS.every(({ key }) => !values[key] || valueOf[key](task) === values[key])

/** The distinct values each filter can take among the loaded tasks, sorted. */
export function sheetFilterOptions(tasks: TaskDTO[]): Record<SheetFilterKey, string[]> {
  const options = Object.fromEntries(SHEET_FILTERS.map(({ key }) => [key, new Set<string>()])) as Record<SheetFilterKey, Set<string>>
  for (const task of tasks) {
    for (const { key } of SHEET_FILTERS) {
      const value = valueOf[key](task)
      if (value) options[key].add(value)
    }
  }
  return Object.fromEntries(SHEET_FILTERS.map(({ key }) => [key, [...options[key]].sort((a, b) => a.localeCompare(b))])) as Record<SheetFilterKey, string[]>
}

/** "Service svc-b · Cluster cluster-b" for the summary row; empty when no sheet filter is set. */
export const summariseSheetFilters = (values: SheetFilterValues) =>
  SHEET_FILTERS.filter(({ key }) => values[key])
    .map(({ key, label }) => `${label} ${values[key]}`)
    .join(' · ')

interface FiltersButtonProps {
  /** Number of sheet filters in use; shown in the name. */
  active: number
  onOpen: () => void
}

/** The "Filters" icon button beside the filter input. */
export function FiltersButton({ active, onOpen }: FiltersButtonProps) {
  return (
    <button type="button" className={styles.button} aria-label={active > 0 ? `Filters (${active})` : 'Filters'} aria-haspopup="dialog" onClick={onOpen}>
      <Icon name="sliders" size={18} />
      {active > 0 && (
        <span className={styles.badge} aria-hidden="true">
          {active}
        </span>
      )}
    </button>
  )
}

interface TasksFiltersProps {
  open: boolean
  onClose: () => void
  options: Record<SheetFilterKey, string[]>
  values: SheetFilterValues
  onChange: (key: SheetFilterKey, value: string) => void
  group: boolean
  onGroupChange: (on: boolean) => void
  visibleColumns: string[]
  onVisibleColumnsChange: (keys: string[]) => void
  defaultColumns: string[]
  onClear: () => void
}

/** Phone sheet for what the desktop toolbar holds: the extra filters, Group repeats and the fields on each row. */
export function TasksFilters({
  open,
  onClose,
  options,
  values,
  onChange,
  group,
  onGroupChange,
  visibleColumns,
  onVisibleColumnsChange,
  defaultColumns,
  onClear,
}: TasksFiltersProps) {
  const isOn = (column: (typeof TASK_COLUMNS)[number]) => Boolean(column.locked) || visibleColumns.includes(column.key)
  const toggleColumn = (key: string) => {
    const next = new Set(visibleColumns)
    if (next.has(key)) next.delete(key)
    else next.add(key)
    onVisibleColumnsChange(TASK_COLUMNS.filter((column) => column.locked || next.has(column.key)).map((column) => column.key))
  }
  return (
    <BottomSheet
      open={open}
      onClose={onClose}
      label="Filters and fields"
      title="Filters and fields"
      tall
      footer={
        <>
          <button type="button" className={styles.action} onClick={onClear}>
            Clear filters
          </button>
          <button type="button" className={`${styles.action} ${styles.primary}`} onClick={onClose}>
            Done
          </button>
        </>
      }
    >
      <div className={styles.stack}>
        {SHEET_FILTERS.map(({ key, label }) => (
          <label key={key} className={styles.field}>
            <span>{label}</span>
            <select className={styles.select} value={values[key]} onChange={(event) => onChange(key, event.target.value)}>
              <option value="">Any</option>
              {options[key].map((option) => (
                <option key={option} value={option}>
                  {option}
                </option>
              ))}
            </select>
          </label>
        ))}
        <Toggle checked={group} onChange={onGroupChange}>
          Group repeats
        </Toggle>
        <div className={styles.fields} role="group" aria-label="Fields on each row">
          <div className={styles.spread}>
            <span className={styles.lab}>Fields on each row</span>
            <span className={styles.count}>
              {TASK_COLUMNS.filter(isOn).length} of {TASK_COLUMNS.length}
            </span>
          </div>
          <span className={styles.count}>Task, Service and Status always show.</span>
          {TASK_COLUMNS.map((column) => (
            <Toggle key={column.key} checked={isOn(column)} disabled={column.locked} onChange={() => toggleColumn(column.key)}>
              {column.label}
            </Toggle>
          ))}
          <button type="button" className={styles.link} onClick={() => onVisibleColumnsChange(defaultColumns)}>
            Reset fields
          </button>
        </div>
      </div>
    </BottomSheet>
  )
}
