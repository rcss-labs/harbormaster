import { Skeleton } from '../../../components/Skeleton'
import type { TimelineEvent } from '../../../api/timeline'
import { LANES, axisTicks, formatEventTime, type MarkerGroup, type TimelineLane } from './model'
import styles from './timeline.module.css'

/** What one lane row shows: markers when `on`, and a message in place of or beside them. */
export interface LaneRowState {
  on: boolean
  message?: string
  loading?: boolean
  onRetry?: () => void
}

interface TimelineLanesProps {
  groups: Record<TimelineLane, MarkerGroup[]>
  /** Range bounds in ms. */
  from: number
  to: number
  selectedId: string | null
  laneStates: Record<TimelineLane, LaneRowState>
  onSelect: (group: MarkerGroup) => void
}

const LANE_ORDER = Object.keys(LANES) as TimelineLane[]
const DAY_MS = 24 * 3_600_000

const markerClass: Record<TimelineEvent['severity'], string> = {
  bad: styles.bad,
  warn: styles.warn,
  change: styles.acc,
  info: styles.info,
}

export function TimelineLanes({ groups, from, to, selectedId, laneStates, onSelect }: TimelineLanesProps) {
  const withDate = to - from > DAY_MS
  return (
    <div className={styles.scroll}>
      <div className={styles.track} data-testid="timeline-track">
        {LANE_ORDER.map((lane) => {
          const state = laneStates[lane]
          if (!state.on && !state.message) return null
          return (
            <div key={lane} className={styles.laneRow}>
              <div className={styles.laneLabel}>{LANES[lane]}</div>
              <div className={styles.laneTrack}>
                {state.loading && <Skeleton lines={1} height={10} label={`Loading ${LANES[lane].toLowerCase()}`} />}
                {state.on &&
                  !state.loading &&
                  groups[lane].map((group) => {
                    const first = group.items[0]
                    const label = `${formatEventTime(first.at, withDate)} ${first.title}`
                    const selected = group.items.some((item) => item.id === selectedId)
                    return (
                      <button
                        key={first.id}
                        type="button"
                        className={[styles.marker, markerClass[group.severity], selected ? styles.sel : ''].filter(Boolean).join(' ')}
                        style={{ left: `${group.pct}%` }}
                        title={label}
                        aria-label={label}
                        onClick={() => onSelect(group)}
                      >
                        {group.items.length > 1 && group.items.length}
                      </button>
                    )
                  })}
                {state.message && (
                  <span className={styles.laneNote}>
                    {state.message}
                    {state.onRetry && (
                      <button type="button" className={styles.retry} onClick={state.onRetry}>
                        Retry
                      </button>
                    )}
                  </span>
                )}
              </div>
            </div>
          )
        })}
        <div className={styles.axis} aria-hidden="true">
          {axisTicks(from, to).map((tick) => (
            <span key={tick.pct} className={styles.tick} style={{ left: `${tick.pct}%` }}>
              {tick.label}
            </span>
          ))}
        </div>
      </div>
    </div>
  )
}
