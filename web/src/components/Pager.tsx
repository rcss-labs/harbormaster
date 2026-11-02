import styles from './Pager.module.css'

interface PagerProps {
  /** 1-based current page. */
  page: number
  pageSize: number
  total: number
  onPageChange: (page: number) => void
}

export function Pager({ page, pageSize, total, onPageChange }: PagerProps) {
  const pageCount = Math.max(1, Math.ceil(total / pageSize))
  if (total <= pageSize) return null
  const current = Math.min(Math.max(1, page), pageCount)
  const first = (current - 1) * pageSize + 1
  const last = Math.min(total, current * pageSize)
  return (
    <nav className={styles.pager} aria-label="Pagination">
      <span className={styles.range}>
        {first}–{last} of {total}
      </span>
      <button type="button" aria-label="Previous page" disabled={current <= 1} onClick={() => onPageChange(current - 1)}>
        Prev
      </button>
      <span className={styles.count}>
        {current} / {pageCount}
      </span>
      <button type="button" aria-label="Next page" disabled={current >= pageCount} onClick={() => onPageChange(current + 1)}>
        Next
      </button>
    </nav>
  )
}
