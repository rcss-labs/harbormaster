import { useEffect, useRef, useState } from 'react'
import { Link, useNavigate } from 'react-router'
import { useSearch } from '../../api/hooks'
import styles from './shell.module.css'

const MAX_RESULTS = 8

export function Search() {
  const navigate = useNavigate()
  const wrap = useRef<HTMLDivElement>(null)
  const [text, setText] = useState('')
  const [open, setOpen] = useState(false)
  const query = text.trim()
  const { data } = useSearch(query)
  const items = (data?.items ?? []).slice(0, MAX_RESULTS)

  useEffect(() => {
    const onDocumentClick = (event: MouseEvent) => {
      if (!wrap.current?.contains(event.target as Node)) setOpen(false)
    }
    document.addEventListener('click', onDocumentClick)
    return () => document.removeEventListener('click', onDocumentClick)
  }, [])

  const finish = () => {
    setText('')
    setOpen(false)
  }

  return (
    <div className={styles.searchWrap} ref={wrap}>
      <input
        className={styles.search}
        type="search"
        placeholder="Jump to cluster, service, task"
        aria-label="Search"
        value={text}
        onChange={(e) => {
          setText(e.target.value)
          setOpen(true)
        }}
        onKeyDown={(e) => {
          if (e.key === 'Escape') setOpen(false)
          if (e.key === 'Enter' && open && items.length > 0) {
            navigate(items[0].href)
            finish()
          }
        }}
      />
      {open && query.length >= 2 && data && (
        <div className={styles.searchResults}>
          {items.length === 0 && <div className={styles.searchEmpty}>No match</div>}
          {items.map((item, index) => (
            <Link key={`${item.kind}${item.href}${item.label}`} to={item.href} className={index === 0 ? styles.sel : undefined} onClick={finish}>
              <span className={styles.kind}>{item.kind}</span>
              <span>{item.label}</span>
              <span className={styles.searchSub}>{item.sub}</span>
            </Link>
          ))}
        </div>
      )}
    </div>
  )
}
