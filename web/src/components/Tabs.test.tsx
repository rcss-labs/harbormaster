import { render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { describe, expect, it } from 'vitest'
import { Tabs } from './Tabs'

const renderTabs = (items: Parameters<typeof Tabs>[0]['items']) =>
  render(
    <MemoryRouter>
      <Tabs items={items} active="a" hrefFor={(key) => `/${key}`} />
    </MemoryRouter>,
  )

describe('Tabs', () => {
  it('renders a gap and the group label before the first item of a group, and a gap when the group ends', () => {
    renderTabs([
      { key: 'a', label: 'Alpha' },
      { key: 'b', label: 'Beta', group: 'Configure' },
      { key: 'c', label: 'Gamma', group: 'Configure' },
      { key: 'd', label: 'Delta' },
    ])
    expect(screen.getAllByText('Configure')).toHaveLength(1)
    expect(screen.getAllByRole('separator')).toHaveLength(2)
  })

  it('without groups renders no separators or labels', () => {
    renderTabs([
      { key: 'a', label: 'Alpha' },
      { key: 'b', label: 'Beta' },
    ])
    expect(screen.queryAllByRole('separator')).toHaveLength(0)
    expect(screen.getAllByRole('link')).toHaveLength(2)
  })
})
