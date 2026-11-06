import { fireEvent, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
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
      // Offsets are bounded so the 190 px tooltip (170 px minimum plus its margins) stays inside 200 px.
      const offset = parseFloat(tip.style.left || tip.style.right)
      const unit = (tip.style.left || tip.style.right).endsWith('px') ? 'px' : '%'
      const px = unit === 'px' ? offset : (offset / 100) * 200
      expect(px).toBeGreaterThanOrEqual(0)
      expect(px).toBeLessThanOrEqual(10)
    }
  })
})
