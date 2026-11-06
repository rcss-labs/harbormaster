import { useEffect, useLayoutEffect, useRef, useState } from 'react'
import { useLogHistory } from '../../../api/observe'
import type { LogLine, LogsHistoryDTO } from '../../../api/types'
import { Banner } from '../../../components/Banner'
import { Panel } from '../../../components/Panel'
import { Seg, SegmentedFilter } from '../../../components/Seg'
import { queryPanelError } from '../panelError'
import { useAuth } from '../../auth/AuthProvider'
import { level } from './level'
import { useLogTail } from './useLogTail'
import styles from './logs.module.css'

// End reasons with their own banner; every other ended reason gets the generic disconnected banner with Resume.
const HANDLED_END_REASONS = new Set(['idle', 'csrf_invalid', 'tail_limit', 'forbidden', 'invalid'])
const SEARCH_DEBOUNCE_MS = 400
const NEAR_BOTTOM_PX = 24

const clock = (iso: string): string => {
  const date = new Date(iso)
  return [date.getHours(), date.getMinutes(), date.getSeconds()].map((n) => String(n).padStart(2, '0')).join(':')
}

type LevelFilter = 'all' | 'warn' | 'error'

const LEVEL_OPTIONS: { value: LevelFilter; label: string }[] = [
  { value: 'all', label: 'All' },
  { value: 'warn', label: 'Warn+' },
  { value: 'error', label: 'Errors' },
]

/** A line passes when its detected severity is at least the chosen level; errors outrank warnings. */
function passesLevel(message: string, filter: LevelFilter): boolean {
  if (filter === 'all') return true
  const severity = level(message)
  return filter === 'warn' ? severity !== '' : severity === 'error'
}

/** One line per row. Time and stream never wrap; a long message is cut with an ellipsis and kept whole in its title. */
function LogRow({ line }: { line: LogLine }) {
  const severity = level(line.message)
  const stream = `${line.container}/${line.task_id.slice(0, 8)}`
  return (
    <div className={styles.ln} role="row" data-level={severity || undefined}>
      <span className={styles.ts} role="cell">
        {clock(line.t)}
      </span>
      <span className={styles.ct} role="cell" title={`${line.container}/${line.task_id}`}>
        {stream}
      </span>
      <span className={styles.msg} role="cell" title={line.message}>
        {line.message}
      </span>
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
  const [levelFilter, setLevelFilter] = useState<LevelFilter>('all')
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
  const visible = levelFilter === 'all' ? allLines : allLines.filter((line) => passesLevel(line.message, levelFilter))
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
    return (
      <Panel title="Logs" error={queryPanelError(newest, 'The logs could not be loaded.')}>
        {null}
      </Panel>
    )
  }
  if (!info) {
    return (
      <Panel title="Logs" loading>
        {null}
      </Panel>
    )
  }

  const driverBanners = unsupported.map((c) => (
    <Banner key={c.name} kind="warn">
      {c.reason === 'other_group'
        ? `${c.name} logs go to a different log group. Harbormaster cannot show those logs.`
        : `${c.name} logs go to the ${c.driver} driver. Harbormaster cannot show those logs.`}
    </Banner>
  ))

  if (!canTail) return <div className={styles.banners}>{driverBanners}</div>

  return (
    <section className={`${styles.root} hm-fill-col`} aria-label="Logs">
      <div className={styles.toolbar}>
        <select className={styles.ctl} aria-label="Container" value={container} onChange={(event) => setContainer(event.target.value)}>
          <option value="all">All containers</option>
          {containers.map((c) => (
            <option key={c.name} value={c.name} disabled={!c.supported} title={c.supported ? undefined : `${c.driver}: Harbormaster cannot show these logs`}>
              {c.name}
            </option>
          ))}
        </select>
        <SegmentedFilter label="Level" options={LEVEL_OPTIONS} value={levelFilter} onChange={setLevelFilter} />
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
        <Seg
          label="Tail"
          options={[
            { value: 'live', label: 'Live' },
            { value: 'paused', label: 'Paused' },
          ]}
          value={follow ? 'live' : 'paused'}
          onChange={(next) => setFollow(next === 'live')}
        />
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
        {follow && tail.status === 'ended' && !HANDLED_END_REASONS.has(tail.endReason ?? '') && (
          <Banner kind="warn">
            {tail.endMessage || 'Live tail disconnected.'}
            <button type="button" className={`${styles.btn} ${styles.bannerAction}`} onClick={tail.resume}>
              Resume
            </button>
          </Banner>
        )}
      </div>
      <div className={`${styles.logs} hm-fill-scroll`} ref={boxRef} role="log" onScroll={onScroll} onWheel={keepalive} onPointerDown={keepalive} onKeyDown={keepalive} onTouchMove={keepalive}>
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
    </section>
  )
}
