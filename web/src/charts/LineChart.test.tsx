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
