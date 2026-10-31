import { useEffect, useLayoutEffect, useRef, useState } from 'react'
import { useLogHistory } from '../../../api/observe'
import type { LogLine, LogsHistoryDTO } from '../../../api/types'
import { Banner } from '../../../components/Banner'
import { useAuth } from '../../auth/AuthProvider'
import { level } from './level'
import { useLogTail } from './useLogTail'
import styles from './logs.module.css'

const SEARCH_DEBOUNCE_MS = 400
const NEAR_BOTTOM_PX = 24

const clock = (iso: string): string => {
  const date = new Date(iso)
  return [date.getHours(), date.getMinutes(), date.getSeconds()].map((n) => String(n).padStart(2, '0')).join(':')
}

function LogRow({ line }: { line: LogLine }) {
  const severity = level(line.message)
  return (
    <div className={styles.ln}>
      <span className={styles.ts}>{clock(line.t)}</span>
      <span className={styles.ct}>{`${line.container}/${line.task_id.slice(0, 8)}`}</span>
      <span className={severity ? styles[`lvl-${severity}`] : undefined}>{line.message}</span>
    </div>
  )
}

interface OlderState {
  scope: string
  before?: string
  lines: LogLine[]
}

/** Logs tab: CloudWatch history (paged backwards), search, level filter and a live tail that follows the bottom. */
export function LogsTab({ serviceId }: { serviceId: string }) {
  const { me } = useAuth()
  const [container, setContainer] = useState('all')
  const [levelFilter, setLevelFilter] = useState<'all' | 'problems'>('all')
  const [searchInput, setSearchInput] = useState('')
  const [q, setQ] = useState('')
  const [follow, setFollow] = useState(true)
  const boxRef = useRef<HTMLDivElement>(null)
  const autoScrollTopRef = useRef<number | null>(null)

  useEffect(() => {
    const timer = window.setTimeout(() => setQ(searchInput.trim()), SEARCH_DEBOUNCE_MS)
    return () => window.clearTimeout(timer)
  }, [searchInput])

  const containerParam = container === 'all' ? undefined : container
  const scope = `${serviceId}|${containerParam ?? ''}|${q}`
  const [stored, setStored] = useState<OlderState>({ scope, lines: [] })
  const older: OlderState = stored.scope === scope ? stored : { scope, lines: [] }

  const newest = useLogHistory(serviceId, { container: containerParam, q: q || undefined })
  const olderPage = useLogHistory(serviceId, { container: containerParam, q: q || undefined, before: older.before })
  // Until the older page arrives, the page shown for `before` is still the newest one.
  const currentPage = older.before ? olderPage : newest

  // Remember the container list so the select does not flicker while a filtered history reloads.
  const [meta, setMeta] = useState<{ serviceId: string; data: LogsHistoryDTO } | null>(null)
  if (newest.data && (meta?.serviceId !== serviceId || meta.data !== newest.data)) setMeta({ serviceId, data: newest.data })
  const info = newest.data ?? (meta?.serviceId === serviceId ? meta.data : undefined)

  const containers = info?.containers ?? []
  const supported = containers.filter((c) => c.supported)
  const unsupported = containers.filter((c) => !c.supported)
  const canTail = info !== undefined && supported.length > 0

  const tail = useLogTail({ serviceId, container: containerParam, q: q || undefined, enabled: follow && canTail, csrf: me?.csrf_token ?? '' })

  const { keepalive } = tail

  const loadOlder = () => {
    const page = currentPage.data
    if (!page?.older_before) return
    setStored({ scope, before: page.older_before, lines: [...(older.before ? page.lines : newest.data?.lines ?? []), ...older.lines] })
  }

  const historyLines = [...(older.before ? (olderPage.data?.lines ?? []) : (newest.data?.lines ?? [])), ...older.lines]
  const allLines = [...historyLines, ...tail.lines]
  const visible = levelFilter === 'problems' ? allLines.filter((line) => level(line.message) !== '') : allLines
  const olderBefore = currentPage.data?.older_before

  useLayoutEffect(() => {
    const box = boxRef.current
    if (follow && box) {
      box.scrollTop = box.scrollHeight
      autoScrollTopRef.current = box.scrollTop // lets onScroll tell this scroll from the user's
    }
  }, [follow, visible.length])

  useEffect(() => {
    const onVisible = () => {
      if (document.visibilityState === 'visible') keepalive()
    }
    document.addEventListener('visibilitychange', onVisible)
    return () => document.removeEventListener('visibilitychange', onVisible)
  }, [keepalive])

  const onScroll = () => {
    const box = boxRef.current
    // Scroll events from our own follow-to-bottom are not user activity; counting them would keep an unattended tab alive forever.
    if (box && box.scrollTop === autoScrollTopRef.current) return
    if (follow && box && box.scrollHeight - box.scrollTop - box.clientHeight > NEAR_BOTTOM_PX) setFollow(false)
  }

  const statusText = !follow ? 'paused' : tail.status === 'live' ? 'live tail' : tail.status === 'ended' ? 'tail stopped' : 'connecting'

  if (newest.isError && !info) {
    return <Banner kind="bad">{newest.error instanceof Error ? newest.error.message : 'The logs could not be loaded.'}</Banner>
  }
  if (!info) return null

  const driverBanners = unsupported.map((c) => (
    <Banner key={c.name} kind="warn">
      {c.reason === 'other_group'
        ? `${c.name} logs go to a different log group. Harbormaster cannot show those logs.`
        : `${c.name} logs go to the ${c.driver} driver. Harbormaster cannot show those logs.`}
    </Banner>
  ))

  if (!canTail) return <div className={styles.banners}>{driverBanners}</div>

  return (
    <>
      <div className={styles.toolbar}>
        <select className={styles.ctl} aria-label="Container" value={container} onChange={(event) => setContainer(event.target.value)}>
          <option value="all">All containers</option>
          {containers.map((c) => (
            <option key={c.name} value={c.name} disabled={!c.supported} title={c.supported ? undefined : `${c.driver}: Harbormaster cannot show these logs`}>
              {c.name}
            </option>
          ))}
        </select>
        <select className={styles.ctl} aria-label="Level" value={levelFilter} onChange={(event) => setLevelFilter(event.target.value as 'all' | 'problems')}>
          <option value="all">All lines</option>
          <option value="problems">Warnings and errors</option>
        </select>
        <input
          className={`${styles.ctl} ${styles.search}`}
          aria-label="Search logs"
          placeholder="Search logs"
          value={searchInput}
          onChange={(event) => {
            setSearchInput(event.target.value)
            tail.keepalive()
          }}
        />
        <button type="button" className={styles.btn} onClick={() => setFollow((on) => !on)}>
          {follow ? 'Pause' : 'Follow'}
        </button>
        <span className={styles.status}>
          CloudWatch Logs · <span className={styles.mono}>{info.group}</span> · {statusText}
        </span>
      </div>
      <div className={styles.banners}>
        {driverBanners}
        {follow && tail.status === 'ended' && tail.endReason === 'idle' && (
          <Banner kind="info">
            Live tail stopped after 30 minutes idle.
            <button type="button" className={`${styles.btn} ${styles.bannerAction}`} onClick={tail.resume}>
              Resume
            </button>
          </Banner>
        )}
        {follow && tail.status === 'ended' && tail.endMessage && (tail.endReason === 'tail_limit' || tail.endReason === 'forbidden' || tail.endReason === 'invalid') && (
          <Banner kind="warn">{tail.endMessage}</Banner>
        )}
        {follow && tail.status === 'ended' && tail.endReason === 'csrf_invalid' && (
          <Banner kind="warn">Your session changed. Reload the page to follow logs again.</Banner>
        )}
        {follow && tail.status === 'ended' && !tail.endMessage && tail.endReason !== 'idle' && tail.endReason !== 'csrf_invalid' && (
          <Banner kind="warn">
            Live tail disconnected.
            <button type="button" className={`${styles.btn} ${styles.bannerAction}`} onClick={tail.resume}>
              Resume
            </button>
          </Banner>
        )}
      </div>
      <div className={styles.logs} ref={boxRef} role="log" onScroll={onScroll} onWheel={keepalive} onPointerDown={keepalive} onKeyDown={keepalive} onTouchMove={keepalive}>
        {olderBefore && (
          <button type="button" className={`${styles.btn} ${styles.older}`} onClick={loadOlder}>
            Load older
          </button>
        )}
        {visible.length === 0 && <div className={styles.empty}>No log lines.</div>}
        {visible.map((line, index) => (
          <LogRow key={`${line.t}|${line.task_id}|${index}`} line={line} />
        ))}
      </div>
    </>
  )
}
