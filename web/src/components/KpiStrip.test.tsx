import { render, screen, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
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

  it('href renders the cell as a link', () => {
    render(
      <MemoryRouter>
        <KpiStrip
          label="k"
          items={[
            { label: 'Failing', value: 4, href: '/services?status=failing' },
            { label: 'Services', value: 12 },
          ]}
        />
      </MemoryRouter>,
    )
    const link = screen.getByRole('link', { name: /Failing/ })
    expect(link).toHaveAttribute('href', '/services?status=failing')
    expect(link).toHaveTextContent('4')
    expect(screen.getAllByRole('link')).toHaveLength(1)
  })

  it('renders a plain anchor when there is no router', () => {
    render(<KpiStrip label="k" items={[{ label: 'Failing', value: 4, href: '/services' }]} />)
    expect(screen.getByRole('link', { name: /Failing/ })).toHaveAttribute('href', '/services')
  })

  it('applies the tone class to the cell', () => {
    render(<KpiStrip label="k" items={[{ label: 'Failing', value: 4, tone: 'bad' }, { label: 'Fine', value: 1 }]} />)
    const [failing, fine] = screen.getAllByRole('listitem')
    expect(failing).toHaveClass('tone-bad')
    expect(fine.className).not.toMatch(/tone-/)
  })
})
