import { screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { renderWithProviders } from '../test/setup'
import { BarChart } from './BarChart'
import { Donut } from './Donut'
import { HBars } from './HBars'
import { LineChart } from './LineChart'
import { Timeline } from './Timeline'

describe('chart primitives', () => {
  it('LineChart renders one path per series and 3 y labels', () => {
    const { container } = renderWithProviders(
      <LineChart
        series={[
          { name: 'cpu', color: 'red', values: [0, 50, 100] },
          { name: 'mem', color: 'blue', values: [10, 20, 30] },
        ]}
        xLabels={['start', 'middle', 'now']}
      />,
    )
    const strokePaths = container.querySelectorAll('path.line')
    expect(strokePaths).toHaveLength(2)
    expect(screen.getByText('0%')).toBeInTheDocument()
    expect(screen.getByText('50%')).toBeInTheDocument()
    expect(screen.getByText('100%')).toBeInTheDocument()
  })

  it('LineChart collapses 10 series to 7 + others', () => {
    const series = Array.from({ length: 10 }, (_, i) => ({
      name: `s${i}`,
      color: `c${i}`,
      values: [i, i],
    }))
    const { container } = renderWithProviders(<LineChart series={series} xLabels={['a', 'b']} />)
    const strokeColours = Array.from(container.querySelectorAll('path.line')).map((path) => path.getAttribute('stroke'))
    expect(strokeColours).toEqual(['c3', 'c4', 'c5', 'c6', 'c7', 'c8', 'c9', 'var(--border-strong)'])
  })

  it('BarChart stacks parts bottom-up', () => {
    const { container } = renderWithProviders(
      <BarChart
        cols={[{ title: 'Mon', parts: [{ v: 30, color: 'red' }, { v: 20, color: 'blue' }] }]}
        xLabels={[[0, 'Mon']]}
      />,
    )
    const [bottomPart, topPart] = Array.from(container.querySelectorAll('rect'))
    expect(Number(bottomPart.getAttribute('y'))).toBeGreaterThan(Number(topPart.getAttribute('y')))
    expect(Number(bottomPart.getAttribute('y')) + Number(bottomPart.getAttribute('height'))).toBe(100)
  })

  it('Donut legend shows counts', () => {
    renderWithProviders(
      <Donut
        segments={[
          { name: 'Steady', v: 5, color: 'green' },
          { name: 'Failing', v: 3, color: 'red' },
          { name: 'Degraded', v: 0, color: 'orange' },
        ]}
        label="8"
        sub="services"
      />,
    )
    expect(screen.getByText('Failing').parentElement).toHaveTextContent('Failing3')
    expect(screen.getByText('Degraded').parentElement).toHaveTextContent('Degraded0')
  })

  it('HBars shows "Nothing to show." when empty', () => {
    renderWithProviders(<HBars items={[]} />)
    expect(screen.getByText('Nothing to show.')).toBeInTheDocument()
  })

  it('Timeline places an event at the right percentage', () => {
    renderWithProviders(<Timeline events={[{ ts: 50, label: ':12', color: 'red' }]} start={0} end={100} />)
    expect(screen.getByText(':12').parentElement).toHaveStyle({ left: '50%' })
  })
})
