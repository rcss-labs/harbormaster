import { useCallback, useState, type KeyboardEvent, type MouseEvent } from 'react'

/** Props to spread on the focusable plot element. */
export interface ChartHoverBind {
  tabIndex: 0
  onMouseMove: (event: MouseEvent<HTMLElement>) => void
  onMouseLeave: () => void
  onFocus: () => void
  onBlur: () => void
  onKeyDown: (event: KeyboardEvent<HTMLElement>) => void
}

/**
 * Tracks which of `count` evenly spaced points is under the pointer or the keyboard cursor.
 * Pointer index = round(x / width * (count - 1)); arrow keys step, Home/End jump, Escape clears.
 */
export function useChartHover(count: number): { index: number | null; bind: ChartHoverBind } {
  const [raw, setRaw] = useState<number | null>(null)
  const index = raw !== null && raw < count ? raw : null
  const clamp = useCallback((i: number) => Math.max(0, Math.min(count - 1, i)), [count])

  const bind: ChartHoverBind = {
    tabIndex: 0,
    onMouseMove: (event) => {
      if (count === 0) return
      const rect = event.currentTarget.getBoundingClientRect()
      if (rect.width <= 0) return
      setRaw(count === 1 ? 0 : clamp(Math.round(((event.clientX - rect.left) / rect.width) * (count - 1))))
    },
    onMouseLeave: () => setRaw(null),
    onFocus: () => setRaw((current) => current ?? (count > 0 ? count - 1 : null)),
    onBlur: () => setRaw(null),
    onKeyDown: (event) => {
      if (count === 0) return
      const step = (delta: number) => setRaw((current) => clamp((current ?? count - 1) + delta))
      switch (event.key) {
        case 'ArrowLeft':
          step(-1)
          break
        case 'ArrowRight':
          step(1)
          break
        case 'Home':
          setRaw(0)
          break
        case 'End':
          setRaw(count - 1)
          break
        case 'Escape':
          setRaw(null)
          return
        default:
          return
      }
      event.preventDefault()
    },
  }
  return { index, bind }
}
