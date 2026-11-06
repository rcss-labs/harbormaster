import { useCallback, useState, type KeyboardEvent, type MouseEvent, type TouchEvent, type FocusEvent } from 'react'

/** Props to spread on the focusable plot element. */
export interface ChartHoverBind {
  tabIndex: 0
  onMouseMove: (event: MouseEvent<HTMLElement>) => void
  onMouseLeave: () => void
  onTouchStart: (event: TouchEvent<HTMLElement>) => void
  onTouchMove: (event: TouchEvent<HTMLElement>) => void
  onFocus: (event: FocusEvent<HTMLElement>) => void
  onBlur: () => void
  onKeyDown: (event: KeyboardEvent<HTMLElement>) => void
}

/**
 * Tracks which of `count` evenly spaced points is under the pointer or the keyboard cursor.
 * Pointer index = round(x / width * (count - 1)); a touch works like the pointer, so a tap shows the tooltip on phones;
 * arrow keys step, Home/End jump, Escape clears. `plotWidth` is the plot's last measured width in px, for clamping the tooltip.
 */
export function useChartHover(count: number): { index: number | null; plotWidth: number | null; bind: ChartHoverBind } {
  const [raw, setRaw] = useState<number | null>(null)
  const [plotWidth, setPlotWidth] = useState<number | null>(null)
  const index = raw !== null && raw < count ? raw : null
  const clamp = useCallback((i: number) => Math.max(0, Math.min(count - 1, i)), [count])

  const pointAt = (plot: HTMLElement, clientX: number | undefined) => {
    if (count === 0 || clientX === undefined) return
    const rect = plot.getBoundingClientRect()
    if (rect.width <= 0) return
    setPlotWidth(rect.width)
    setRaw(count === 1 ? 0 : clamp(Math.round(((clientX - rect.left) / rect.width) * (count - 1))))
  }

  const bind: ChartHoverBind = {
    tabIndex: 0,
    onMouseMove: (event) => pointAt(event.currentTarget, event.clientX),
    onTouchStart: (event) => pointAt(event.currentTarget, event.touches[0]?.clientX),
    onTouchMove: (event) => pointAt(event.currentTarget, event.touches[0]?.clientX),
    onMouseLeave: () => setRaw(null),
    onFocus: (event) => {
      const width = event.currentTarget.getBoundingClientRect().width
      if (width > 0) setPlotWidth(width)
      setRaw((current) => current ?? (count > 0 ? count - 1 : null))
    },
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
  return { index, plotWidth, bind }
}
