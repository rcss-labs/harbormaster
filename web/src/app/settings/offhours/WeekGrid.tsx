import { useEffect, useRef, useState } from 'react'
import type { AwakeGrid } from '../../../api/offhours'
import { DAYS } from './offhours'
import styles from './offhours.module.css'

interface WeekGridProps {
  value: AwakeGrid
  onChange: (next: AwakeGrid) => void
  /** The cell that holds the current hour, outlined. */
  now?: { day: number; hour: number } | null
}

const HOURS = Array.from({ length: 24 }, (_, hour) => hour)

/**
 * The 7x24 awake grid. Every cell is a toggle button; one cell is a tab stop and the arrow keys move between cells.
 * Pressing a cell and dragging paints the cells crossed with the value the first cell was switched to.
 */
export function WeekGrid({ value, onChange, now }: WeekGridProps) {
  const [active, setActive] = useState({ day: 0, hour: 0 })
  const cells = useRef(new Map<string, HTMLButtonElement>())
  // Drag painting calls onChange several times before React re-renders, so it works from the latest grid.
  const latest = useRef(value)
  useEffect(() => {
    latest.current = value
  }, [value])
  const painting = useRef<boolean | null>(null)

  useEffect(() => {
    const stop = () => {
      painting.current = null
    }
    window.addEventListener('pointerup', stop)
    window.addEventListener('pointercancel', stop)
    return () => {
      window.removeEventListener('pointerup', stop)
      window.removeEventListener('pointercancel', stop)
    }
  }, [])

  const paint = (day: number, hour: number, awake: boolean) => {
    if (latest.current[day][hour] === awake) return
    const next = latest.current.map((row, d) => (d === day ? row.map((flag, h) => (h === hour ? awake : flag)) : row))
    latest.current = next
    onChange(next)
  }

  const focusCell = (day: number, hour: number) => {
    const target = { day: Math.min(6, Math.max(0, day)), hour: Math.min(23, Math.max(0, hour)) }
    setActive(target)
    cells.current.get(`${target.day}:${target.hour}`)?.focus()
  }

  const onKeyDown = (event: React.KeyboardEvent, day: number, hour: number) => {
    const moves: Record<string, [number, number]> = {
      ArrowRight: [day, hour + 1],
      ArrowLeft: [day, hour - 1],
      ArrowDown: [day + 1, hour],
      ArrowUp: [day - 1, hour],
      Home: [day, 0],
      End: [day, 23],
    }
    const move = moves[event.key]
    if (!move) return
    event.preventDefault()
    focusCell(...move)
  }

  return (
    <div className={styles.week} role="group" aria-label="Awake hours">
      <span />
      {HOURS.map((hour) => (
        <span key={hour} className={styles.hourLabel} aria-hidden="true">
          {hour % 6 === 0 ? String(hour).padStart(2, '0') : ''}
        </span>
      ))}
      {DAYS.map((dayName, day) => (
        <div key={dayName} className={styles.weekRow} role="presentation">
          <span className={styles.dayLabel} aria-hidden="true">
            {dayName}
          </span>
          {HOURS.map((hour) => {
            const awake = value[day][hour]
            const isNow = now?.day === day && now.hour === hour
            return (
              <button
                key={hour}
                type="button"
                ref={(element) => {
                  if (element) cells.current.set(`${day}:${hour}`, element)
                  else cells.current.delete(`${day}:${hour}`)
                }}
                className={`${styles.cell} ${awake ? styles.cellOn : ''}`.trim()}
                aria-pressed={awake}
                aria-label={`${dayName} ${hour}:00 ${awake ? 'awake' : 'asleep'}`}
                tabIndex={active.day === day && active.hour === hour ? 0 : -1}
                data-now={isNow ? 'true' : undefined}
                onFocus={() => setActive({ day, hour })}
                onKeyDown={(event) => onKeyDown(event, day, hour)}
                onPointerDown={(event) => {
                  if (event.button !== 0) return
                  painting.current = !latest.current[day][hour]
                  paint(day, hour, painting.current)
                }}
                onPointerEnter={() => {
                  if (painting.current !== null) paint(day, hour, painting.current)
                }}
                // Mouse and touch toggle on pointer down. A click with no pointer behind it (Enter or Space) toggles here.
                onClick={(event) => {
                  if (event.detail === 0) paint(day, hour, !latest.current[day][hour])
                }}
              />
            )
          })}
        </div>
      ))}
    </div>
  )
}
