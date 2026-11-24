import { fireEvent, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { renderWithProviders } from '../test/setup'
import { TimeSeriesChart } from './TimeSeriesChart'

const NOW = Date.parse('2026-10-31T12:00:00Z')
const HOUR = 3_600_000

const timestamps = (n: number) => Array.from({ length: n }, (_, i) => new Date(NOW - (n - 1 - i) * HOUR).toISOString())

type Props = Parameters<typeof TimeSeriesChart>[0]

function chart(overrides: Partial<Props> = {}) {
  const props: Props = {
    timestamps: timestamps(3),
    series: [
      { key: 'a', label: 'Alpha', color: 'red', values: [10, 20, 40] },
      { key: 'b', label: 'Beta', color: 'blue', values: [5, 5, 10] },
    ],
    mode: 'line',
    xLabels: ['start', 'middle', 'now'],
    now: NOW,
    ...overrides,
  }
  return renderWithProviders(<TimeSeriesChart {...props} />)
}

/** Numeric [x, y] pairs of a path such as "M0.0,150.0 L300.0,75.0". */
const points = (path: Element | null): number[][] =>
  (path?.getAttribute('d') ?? '')
    .match(/-?\d+(?:\.\d+)?,-?\d+(?:\.\d+)?/g)!
    .map((pair) => pair.split(',').map(Number))

const line = (container: HTMLElement, key: string) => container.querySelector(`path[data-line="${key}"]`)

function hover(percent: number) {
  const plot = screen.getByRole('group', { name: 'Chart' })
  plot.getBoundingClientRect = () => ({ left: 0, top: 0, width: 1000, height: 150, right: 1000, bottom: 150, x: 0, y: 0, toJSON: () => ({}) })
  fireEvent.mouseMove(plot, { clientX: percent * 10 })
}

describe('TimeSeriesChart', () => {
  it('renders one path per series and 5 gridlines', () => {
    const { container } = chart()
    expect(container.querySelectorAll('path[data-line]')).toHaveLength(2)
    expect(container.querySelectorAll('line[data-gridline]')).toHaveLength(5)
    expect(screen.getByText('start')).toBeInTheDocument()
    expect(screen.getByText('middle')).toBeInTheDocument()
    expect(screen.getByText('now')).toBeInTheDocument()
  })

  it('stacked mode offsets upper series by lower values', () => {
    const { container } = chart({ mode: 'stacked', yMax: 50 })
    // Series a is the base (top = 10, 20, 40); b sits on top (top = 15, 25, 50).
    expect(points(line(container, 'a')).map(([, y]) => y)).toEqual([120, 90, 30])
    expect(points(line(container, 'b')).map(([, y]) => y)).toEqual([105, 75, 0])
  })

  it('draws a dashed reference line with its label at the value', () => {
    const { container } = chart({ yMax: 50, reference: { value: 25, label: 'target' } })
    const reference = container.querySelector('line[data-reference]')
    expect(reference).toHaveAttribute('y1', '75')
    expect(reference).toHaveAttribute('stroke-dasharray')
    expect(screen.getByText('target')).toBeInTheDocument()
  })

  it('y labels show max and midpoint with yFormat', () => {
    chart({ yMax: 50, yFormat: (v) => `${v} ms` })
    expect(screen.getByText('50 ms')).toBeInTheDocument()
    expect(screen.getByText('25 ms')).toBeInTheDocument()
  })

  it('clamps yMin/yMax', () => {
    const { container } = chart({
      series: [{ key: 'a', label: 'Alpha', color: 'red', values: [90, 98, 120] }],
      yMin: 96,
      yMax: 100,
    })
    // 90 clamps to the bottom edge (150), 98 is the midpoint, 120 clamps to the top edge (0).
    expect(points(line(container, 'a')).map(([, y]) => y)).toEqual([150, 75, 0])
  })

  it('the tooltip of an hourly bucket gives its date and time besides the age', () => {
    const times = timestamps(11)
    chart({ timestamps: times, series: [{ key: 'a', label: 'Alpha', color: 'red', values: Array.from({ length: 11 }, (_, i) => i) }] })
    hover(10)
    const at = new Date(times[1])
    expect(screen.getByRole('tooltip')).toHaveTextContent(at.toLocaleDateString([], { weekday: 'short', month: 'short', day: 'numeric' }))
    expect(screen.getByRole('tooltip')).toHaveTextContent(at.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }))
    expect(screen.getByRole('tooltip')).toHaveTextContent('9 h ago')
  })

  describe('day buckets west of UTC', () => {
    beforeEach(() => vi.stubEnv('TZ', 'America/New_York'))
    afterEach(() => vi.unstubAllEnvs())
    const title = (day: string, year?: 'numeric') => new Date(`${day}T00:00:00`).toLocaleDateString([], { weekday: 'short', month: 'short', day: 'numeric', year })

    it('the tooltip gives the calendar day of the API, with the year once it differs, and no time', () => {
      chart({ timestamps: ['2025-12-30T00:00:00Z', '2025-12-31T00:00:00Z', '2026-01-01T00:00:00Z'], now: Date.parse('2026-01-01T18:00:00Z') })
      hover(0)
      expect(screen.getByText(title('2025-12-30', 'numeric'))).toBeInTheDocument()
    })

    it('a daily series with a single bucket is still a day', () => {
      chart({ timestamps: ['2026-11-21T00:00:00Z'], series: [{ key: 'a', label: 'Alpha', color: 'red', values: [1] }], now: Date.parse('2026-11-24T18:00:00Z') })
      hover(0)
      expect(screen.getByText(title('2026-11-21'))).toBeInTheDocument()
    })
  })

  it('the tooltip stays beside the hovered point on a half-width plot', () => {
    chart({ timestamps: timestamps(101), series: [{ key: 'a', label: 'Alpha', color: 'red', values: Array.from({ length: 101 }, (_, i) => i) }] })
    const plot = screen.getByRole('group', { name: 'Chart' })
    plot.getBoundingClientRect = () => ({ left: 0, top: 0, width: 540, height: 150, right: 540, bottom: 150, x: 0, y: 0, toJSON: () => ({}) })
    // 56% of 540 px leaves less than the tooltip's width on the right, so it must sit on the left of the point.
    fireEvent.mouseMove(plot, { clientX: 0.56 * 540 })
    expect(parseFloat(screen.getByRole('tooltip').style.right)).toBeCloseTo(44)
    // On a phone-width plot it shrinks to the side that has room instead of covering the point.
    plot.getBoundingClientRect = () => ({ left: 0, top: 0, width: 330, height: 150, right: 330, bottom: 150, x: 0, y: 0, toJSON: () => ({}) })
    fireEvent.mouseMove(plot, { clientX: 0.3 * 330 })
    const tip = screen.getByRole('tooltip')
    expect(parseFloat(tip.style.left)).toBeCloseTo(30)
    expect(parseFloat(tip.style.maxWidth)).toBeLessThanOrEqual(0.7 * 330 - 24 + 0.01)
  })

  it('hover at 10% shows tooltip left-anchored; at 70% flips', () => {
    chart({ timestamps: timestamps(11), series: [{ key: 'a', label: 'Alpha', color: 'red', values: Array.from({ length: 11 }, (_, i) => i) }] })
    hover(10)
    const tip = screen.getByRole('tooltip')
    expect(tip.style.left).not.toBe('')
    expect(tip.style.right).toBe('')
    expect(tip).toHaveTextContent('Alpha')
    expect(tip).toHaveTextContent('9 h ago')
    hover(70)
    const flipped = screen.getByRole('tooltip')
    expect(flipped.style.right).not.toBe('')
    expect(flipped.style.left).toBe('')
  })

  it('stacked tooltip includes Total', () => {
    chart({ mode: 'stacked' })
    hover(100)
    const tip = screen.getByRole('tooltip')
    expect(tip).toHaveTextContent('Total')
    expect(tip).toHaveTextContent('50')
  })

  it('non-stacked tooltip has no Total', () => {
    chart()
    hover(100)
    expect(screen.getByRole('tooltip')).not.toHaveTextContent('Total')
  })

  it('null values break the line', () => {
    const { container } = chart({
      timestamps: timestamps(4),
      series: [{ key: 'a', label: 'Alpha', color: 'red', values: [1, null, 3, 4] }],
    })
    const d = line(container, 'a')!.getAttribute('d')!
    expect(d.match(/M/g)).toHaveLength(2)
  })

  it('shows the tooltip on keyboard focus and moves with arrow keys', () => {
    chart()
    const plot = screen.getByRole('group', { name: 'Chart' })
    fireEvent.focus(plot)
    expect(screen.getByRole('tooltip')).toHaveTextContent('now')
    fireEvent.keyDown(plot, { key: 'ArrowLeft' })
    expect(screen.getByRole('tooltip')).toHaveTextContent('1 h ago')
    fireEvent.keyDown(plot, { key: 'Escape' })
    expect(screen.queryByRole('tooltip')).not.toBeInTheDocument()
  })

  it('touch shows the tooltip at the touched point', () => {
    chart()
    const plot = screen.getByRole('group', { name: 'Chart' })
    plot.getBoundingClientRect = () => ({ left: 0, top: 0, width: 1000, height: 150, right: 1000, bottom: 150, x: 0, y: 0, toJSON: () => ({}) })
    expect(screen.queryByRole('tooltip')).not.toBeInTheDocument()
    fireEvent.touchStart(plot, { touches: [{ clientX: 0 }] })
    expect(screen.getByRole('tooltip')).toHaveTextContent('2 h ago')
    fireEvent.touchMove(plot, { touches: [{ clientX: 1000 }] })
    expect(screen.getByRole('tooltip')).toHaveTextContent('now')
  })

  it('tooltip never exceeds the plot on a narrow plot', () => {
    chart()
    const plot = screen.getByRole('group', { name: 'Chart' })
    plot.getBoundingClientRect = () => ({ left: 0, top: 0, width: 200, height: 150, right: 200, bottom: 150, x: 0, y: 0, toJSON: () => ({}) })
    for (const clientX of [0, 100, 150, 200]) {
      fireEvent.mouseMove(plot, { clientX })
      const tip = screen.getByRole('tooltip')
      // Offsets are bounded so the tooltip, shrunk to a plot narrower than its minimum, stays inside 200 px.
      const offset = parseFloat(tip.style.left || tip.style.right)
      const unit = (tip.style.left || tip.style.right).endsWith('px') ? 'px' : '%'
      const px = unit === 'px' ? offset : (offset / 100) * 200
      expect(px).toBeGreaterThanOrEqual(0)
      expect(px).toBeLessThanOrEqual(10)
    }
  })
})
