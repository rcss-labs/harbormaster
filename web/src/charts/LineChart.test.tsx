import { describe, expect, it } from 'vitest'
import { renderWithProviders } from '../test/setup'
import { LineChart } from './LineChart'

const count = (text: string, char: string) => text.split(char).length - 1

describe('LineChart with gaps', () => {
  it('breaks the path at null values', () => {
    const { container } = renderWithProviders(
      <LineChart series={[{ name: 'cpu', color: 'red', values: [1, null, 2, 3] }]} xLabels={['a', 'b']} />,
    )
    const stroke = container.querySelector('path.line')
    expect(count(stroke?.getAttribute('d') ?? '', 'M')).toBe(2)
  })

  it('draws no path when all values are null', () => {
    const { container } = renderWithProviders(
      <LineChart series={[{ name: 'cpu', color: 'red', values: [null, null, null] }]} xLabels={['a', 'b']} />,
    )
    expect(container.querySelectorAll('path')).toHaveLength(0)
    expect(container.querySelectorAll('line').length).toBeGreaterThan(0)
  })
})

describe('LineChart marker labels', () => {
  const render = (at: number) =>
    renderWithProviders(<LineChart series={[{ name: 'cpu', color: 'red', values: [1, 2] }]} xLabels={['a', 'b']} marks={[{ at, label: 'deploy' }]} />)

  it('anchors a label near the right edge inward so it is not clipped', () => {
    const { getByText } = render(0.98)
    expect(getByText('deploy').style.transform).toBe('translateX(-100%)')
  })

  it('anchors a label near the left edge inward', () => {
    const { getByText } = render(0.02)
    expect(getByText('deploy').style.transform).toBe('translateX(0)')
  })

  it('centres a label away from the edges', () => {
    const { getByText } = render(0.5)
    expect(getByText('deploy').style.transform).toBe('')
  })
})
