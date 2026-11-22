import type { FocusDTO } from '../api/focus'
import { Ago } from './Ago'
import { Pill, type PillKind } from './Pill'
import styles from './LiveRefresh.module.css'

/** What the pill says, or null when there is nothing to say yet (no focus read has landed). */
export function pillText(focus: FocusDTO): string | null {
  switch (focus.state) {
    case 'live':
      if (focus.refreshed_at === null) return null
      return focus.interval_seconds <= 5 ? `Live, every ${focus.interval_seconds} s` : `Refreshing every ${focus.interval_seconds} s`
    case 'throttled':
      return 'Slowed by AWS throttling'
    case 'capped':
    case 'user_capped':
      return 'Normal pace (live limit reached)'
    case 'unavailable':
      return 'Live refresh unavailable'
  }
}

function pillKind(focus: FocusDTO): PillKind {
  switch (focus.state) {
    case 'live':
      return 'info'
    case 'throttled':
    case 'unavailable':
      return 'warn'
    default:
      return 'mute'
  }
}

/** The newer of two RFC 3339 times; either may be missing. */
function newer(a?: string | null, b?: string | null): string | undefined {
  if (!a) return b ?? undefined
  if (!b) return a
  return Date.parse(a) >= Date.parse(b) ? a : b
}

interface LiveRefreshProps {
  /** When the slice was last polled. */
  polledAt?: string
  focus?: FocusDTO
}

/** How fresh the page's data is and, while the page is open, what is refreshing it. */
export function LiveRefresh({ polledAt, focus }: LiveRefreshProps) {
  const age = newer(polledAt, focus?.refreshed_at)
  const text = focus ? pillText(focus) : null
  if (!age && !text) return null
  return (
    <span className={styles.root} data-live-refresh>
      {age && (
        <span className={styles.age}>
          Updated <Ago ts={age} />
        </span>
      )}
      <span aria-live="polite">
        {focus && text && (
          <Pill kind={pillKind(focus)} title={focus.message || undefined}>
            {text}
          </Pill>
        )}
      </span>
    </span>
  )
}
