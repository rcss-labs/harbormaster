import styles from './charts.module.css'
import { dayLabel } from './format'

export interface TimelineEvent {
  ts: number
  label: string
  color: string
}

interface TimelineProps {
  events: TimelineEvent[]
  /** Left edge of the axis, epoch ms. */
  start: number
  /** Right edge of the axis, epoch ms. */
  end: number
}

const TICK_COUNT = 8

/** Event strip on a shared time axis with 8 day ticks; events alternate between two label rows (mockup deployTimeline()). */
export function Timeline({ events, start, end }: TimelineProps) {
  const span = end - start
  const percentOf = (ts: number) => Math.min(100, Math.max(0, ((ts - start) / span) * 100))
  const ticks = Array.from({ length: TICK_COUNT }, (_, i) => start + (i / (TICK_COUNT - 1)) * span)

  return (
    <div className={styles.timeline}>
      <div className={styles.axis} />
      {ticks.map((tick, i) => (
        <span key={i} className={styles.tick} style={{ left: `${percentOf(tick)}%` }}>
          {dayLabel(tick)}
        </span>
      ))}
      {events.map((event, i) => (
        <div
          key={`${event.ts}-${i}`}
          className={i % 2 ? `${styles.ev} ${styles.alt}` : styles.ev}
          style={{ left: `${percentOf(event.ts)}%` }}
          title={new Date(event.ts).toLocaleString()}
        >
          <span>{event.label}</span>
          <i style={{ background: event.color }} />
        </div>
      ))}
    </div>
  )
}
