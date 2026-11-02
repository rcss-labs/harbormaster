import { render, screen, within } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { KpiStrip } from './KpiStrip'

describe('KpiStrip', () => {
  it('renders one labelled cell per item with its value', () => {
    render(
      <KpiStrip
        label="Estate summary"
        items={[
          { label: 'Services', value: '612' },
          { label: 'Failing', value: 4 },
        ]}
      />,
    )
    const list = screen.getByRole('list', { name: 'Estate summary' })
    const cells = within(list).getAllByRole('listitem')
    expect(cells).toHaveLength(2)
    expect(cells[0]).toHaveTextContent('Services')
    expect(cells[0]).toHaveTextContent('612')
    expect(cells[1]).toHaveTextContent('4')
  })

  it('draws a sparkline only for cells that have values', () => {
    const { container } = render(
      <KpiStrip
        label="k"
        items={[
          { label: 'CPU', value: '40%', spark: [1, 2, 3] },
          { label: 'Mem', value: '10%' },
        ]}
      />,
    )
    expect(container.querySelectorAll('svg')).toHaveLength(1)
  })
})
