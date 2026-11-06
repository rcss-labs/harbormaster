import { useMemo, useState, type CSSProperties, type KeyboardEvent, type ReactNode } from 'react'
import styles from './DataTable.module.css'
import { Pager } from './Pager'
import { usePhone } from './useNarrow'

export interface DataTableColumn<R> {
  key: string
  header: ReactNode
  /** CSS grid track, e.g. `80px` or `2fr`. Defaults to `1fr`. */
  width?: string
  align?: 'left' | 'right'
  render: (row: R, index: number) => ReactNode
  /** Makes the column sortable. Null/undefined values sort last. */
  sortValue?: (row: R) => string | number | null | undefined
  /**
   * Role of the column in the phone row list. `title` is the row's name (line 1), `status` sits beside it,
   * `meta` fills line 2, `action` renders in the 44 px action column at the right (for tables that carry their actions
   * as an ordinary last column instead of `rowAction`), `hidden` is left out. Default: the first shown column is the title, a column keyed `status`
   * is the status, everything else is meta.
   */
  phone?: PhoneRole
}

export type PhoneRole = 'title' | 'status' | 'meta' | 'action' | 'hidden'

export interface SortState {
  key: string
  dir: 'asc' | 'desc'
}

export type RowStatus = 'bad' | 'warn' | 'info' | 'expected'

interface DataTableProps<R> {
  /** Accessible name of the table. */
  label: string
  columns: DataTableColumn<R>[]
  rows: R[]
  rowKey: (row: R) => string
  /** Content shown instead of rows when `rows` is empty. */
  empty?: ReactNode
  /** Minimum grid width in px; the table scrolls horizontally below it. */
  minWidth?: number
  /** Cap the body height (px or CSS length) so the header stays sticky while rows scroll. */
  maxHeight?: number | string
  /** Return a status for non-healthy rows to draw the 3px left stripe. */
  rowStatus?: (row: R) => RowStatus | undefined
  onRowClick?: (row: R) => void
  /** Controlled sort; pass `null` for unsorted. Omit to let the table keep its own state. */
  sort?: SortState | null
  defaultSort?: SortState | null
  onSortChange?: (sort: SortState) => void
  /** Built-in paging when set. Leave unset and render `Pager` yourself for server or external paging. */
  pageSize?: number
  /** Controlled 1-based page. */
  page?: number
  onPageChange?: (page: number) => void
  /** Pin the last column (the row actions) to the right edge while the table scrolls sideways. */
  stickyActions?: boolean
  /** Keys of the columns to show, in table order; all columns when omitted. */
  visibleColumns?: string[]
  /** Appends an "Actions" column rendering the returned controls for each row. */
  rowAction?: (row: R) => ReactNode
}

const INTERACTIVE = 'a, button, input, select, textarea, [role="menuitem"]'

function isEmptyCell(node: ReactNode): boolean {
  return node === null || node === undefined || node === false || node === ''
}

const ACTIONS_KEY = '__actions'
const BODY_ROW_STYLE: CSSProperties = { minHeight: 'var(--row)' }
const STICKY_STYLE: CSSProperties = { position: 'sticky', right: 0 }

function compare(a: string | number | null | undefined, b: string | number | null | undefined): number {
  if (a == null && b == null) return 0
  if (a == null) return 1
  if (b == null) return -1
  if (typeof a === 'number' && typeof b === 'number') return a - b
  return String(a).localeCompare(String(b), undefined, { numeric: true, sensitivity: 'base' })
}

export function DataTable<R>({
  label,
  columns: allColumns,
  rows,
  rowKey,
  empty,
  minWidth,
  maxHeight,
  rowStatus,
  onRowClick,
  sort,
  defaultSort = null,
  onSortChange,
  pageSize,
  page,
  onPageChange,
  stickyActions = false,
  visibleColumns,
  rowAction,
}: DataTableProps<R>) {
  const columns = useMemo(() => {
    const shown = visibleColumns ? allColumns.filter((column) => visibleColumns.includes(column.key)) : allColumns
    if (!rowAction) return shown
    const actions: DataTableColumn<R> = {
      key: ACTIONS_KEY,
      header: <span className="hm-sr">Actions</span>,
      width: 'max-content',
      align: 'right',
      render: (row) => rowAction(row),
    }
    return [...shown, actions]
  }, [allColumns, visibleColumns, rowAction])
  const phone = usePhone()
  const stickyKey = stickyActions ? columns[columns.length - 1]?.key : undefined
  const [innerSort, setInnerSort] = useState<SortState | null>(defaultSort)
  const [innerPage, setInnerPage] = useState(1)
  const activeSort = sort !== undefined ? sort : innerSort
  const currentPage = page ?? innerPage

  const sorted = useMemo(() => {
    const column = activeSort ? columns.find((c) => c.key === activeSort.key) : undefined
    if (!activeSort || !column?.sortValue) return rows
    const { sortValue } = column
    const factor = activeSort.dir === 'asc' ? 1 : -1
    return [...rows].sort((a, b) => {
      const av = sortValue(a)
      const bv = sortValue(b)
      // Missing values stay last in both directions.
      if (av == null || bv == null) return compare(av, bv)
      return factor * compare(av, bv)
    })
  }, [rows, columns, activeSort])

  const pageCount = pageSize ? Math.max(1, Math.ceil(sorted.length / pageSize)) : 1
  const safePage = Math.min(Math.max(1, currentPage), pageCount)
  const visible = pageSize ? sorted.slice((safePage - 1) * pageSize, safePage * pageSize) : sorted

  const changePage = (next: number) => {
    setInnerPage(next)
    onPageChange?.(next)
  }

  const toggleSort = (key: string) => {
    const next: SortState = activeSort?.key === key && activeSort.dir === 'asc' ? { key, dir: 'desc' } : { key, dir: 'asc' }
    setInnerSort(next)
    setInnerPage(1)
    onSortChange?.(next)
  }

  const template = columns.map((c) => c.width ?? '1fr').join(' ')
  // One grid for the whole table: head and body rows are subgrids, so every row shares the same column tracks.
  const tableStyle: CSSProperties = { gridTemplateColumns: template, ...(minWidth ? { minWidth } : null) }

  const onRowKeyDown = (event: KeyboardEvent<HTMLDivElement>, row: R) => {
    if (event.key === 'Enter' && event.target === event.currentTarget) onRowClick?.(row)
  }

  if (phone) {
    const shown = columns.filter((column) => column.key !== ACTIONS_KEY)
    const explicit = (column: DataTableColumn<R>): PhoneRole | undefined => column.phone
    const titleColumn = shown.find((column) => explicit(column) === 'title') ?? shown.find((column) => explicit(column) === undefined && column.key !== 'status') ?? shown[0]
    const roleOf = (column: DataTableColumn<R>): PhoneRole => {
      if (column === titleColumn) return 'title'
      return explicit(column) ?? (column.key === 'status' ? 'status' : 'meta')
    }
    const statusColumns = shown.filter((column) => roleOf(column) === 'status')
    const metaColumns = shown.filter((column) => roleOf(column) === 'meta')
    const actionColumn = columns.find((column) => column.key === ACTIONS_KEY) ?? shown.find((column) => explicit(column) === 'action')
    return (
      <div className={styles.wrap}>
        <div role="table" aria-label={label} className="hm-lst">
          {visible.length === 0 && empty !== undefined && <div className={styles.empty}>{empty}</div>}
          {visible.map((row, index) => {
            const status = rowStatus?.(row)
            const meta = metaColumns.map((column) => ({ column, content: column.render(row, index) })).filter(({ content }) => !isEmptyCell(content))
            return (
              <div
                key={rowKey(row)}
                role="row"
                className={`hm-lr ${onRowClick ? 'hm-lr-click' : ''}`.trim()}
                data-hm-row=""
                data-status={status}
                tabIndex={onRowClick ? 0 : undefined}
                onClick={onRowClick ? (event) => { if (!(event.target as HTMLElement).closest(INTERACTIVE)) onRowClick(row) } : undefined}
                onKeyDown={onRowClick ? (event) => onRowKeyDown(event, row) : undefined}
              >
                <div className="hm-lr-main">
                  <div className="hm-l1">
                    {titleColumn && (
                      <span role="cell" className="hm-nm" data-hm-cell={titleColumn.key}>
                        {titleColumn.render(row, index)}
                      </span>
                    )}
                    {statusColumns.map((column) => (
                      <span key={column.key} role="cell" data-hm-cell={column.key}>
                        {column.render(row, index)}
                      </span>
                    ))}
                  </div>
                  {meta.length > 0 && (
                    <div className="hm-l2">
                      {meta.map(({ column, content }) => (
                        <span key={column.key} role="cell" data-hm-cell={column.key}>
                          {content}
                        </span>
                      ))}
                    </div>
                  )}
                </div>
                {actionColumn && (
                  <div role="cell" className="hm-lr-act" data-hm-cell={ACTIONS_KEY}>
                    {actionColumn.render(row, index)}
                  </div>
                )}
              </div>
            )
          })}
        </div>
        {pageSize !== undefined && <Pager page={safePage} pageSize={pageSize} total={sorted.length} onPageChange={changePage} />}
      </div>
    )
  }

  return (
    <div className={styles.wrap}>
      <div className={styles.scroll} style={maxHeight !== undefined ? { maxHeight } : undefined}>
        <div role="table" aria-label={label} className={styles.table} style={tableStyle}>
          <div role="rowgroup" className={`${styles.group} ${styles.head}`}>
            <div role="row" className={styles.row}>
              {columns.map((column) => {
                const sortable = column.sortValue !== undefined
                const dir = activeSort?.key === column.key ? activeSort.dir : undefined
                return (
                  <div
                    key={column.key}
                    role="columnheader"
                    className={`${styles.th} ${column.align === 'right' ? styles.right : ''} ${column.key === stickyKey ? styles.sticky : ''}`.trim()}
                    style={column.key === stickyKey ? STICKY_STYLE : undefined}
                    aria-sort={sortable ? (dir === 'asc' ? 'ascending' : dir === 'desc' ? 'descending' : 'none') : undefined}
                  >
                    {sortable ? (
                      <button type="button" className={styles.sortButton} onClick={() => toggleSort(column.key)}>
                        {column.header}
                        <span className={styles.arrow} aria-hidden="true">
                          {dir === 'asc' ? '▲' : dir === 'desc' ? '▼' : ''}
                        </span>
                      </button>
                    ) : (
                      column.header
                    )}
                  </div>
                )
              })}
            </div>
          </div>
          <div role="rowgroup" className={styles.group}>
            {visible.length === 0 && empty !== undefined && <div className={styles.empty}>{empty}</div>}
            {visible.map((row, index) => {
              const status = rowStatus?.(row)
              return (
                <div
                  key={rowKey(row)}
                  role="row"
                  className={`${styles.row} ${styles.body} ${status ? styles[status] : ''} ${onRowClick ? styles.clickable : ''}`.trim()}
                  style={BODY_ROW_STYLE}
                  data-status={status}
                  tabIndex={onRowClick ? 0 : undefined}
                  onClick={onRowClick ? () => onRowClick(row) : undefined}
                  onKeyDown={onRowClick ? (event) => onRowKeyDown(event, row) : undefined}
                >
                  {columns.map((column) => (
                    <div
                      key={column.key}
                      role="cell"
                      className={`${styles.td} ${column.align === 'right' ? styles.right : ''} ${column.key === stickyKey ? styles.sticky : ''}`.trim()}
                      style={column.key === stickyKey ? STICKY_STYLE : undefined}
                    >
                      {column.render(row, index)}
                    </div>
                  ))}
                </div>
              )
            })}
          </div>
        </div>
      </div>
      {pageSize !== undefined && <Pager page={safePage} pageSize={pageSize} total={sorted.length} onPageChange={changePage} />}
    </div>
  )
}
