import { render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { afterEach, describe, expect, it, vi } from 'vitest'
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

  describe('scrolling the open tab into view', () => {
    afterEach(() => vi.restoreAllMocks())

    it('centres the active tab in the strip, and follows when the active tab changes', () => {
      // jsdom has no layout: give the strip a 100 px window and each link a position by its label.
      const offsets: Record<string, number> = { Alpha: 0, Beta: 300, Gamma: 600 }
      vi.spyOn(HTMLElement.prototype, 'offsetLeft', 'get').mockImplementation(function (this: HTMLElement) {
        return offsets[this.textContent ?? ''] ?? 0
      })
      vi.spyOn(HTMLElement.prototype, 'offsetWidth', 'get').mockImplementation(function (this: HTMLElement) {
        return this.tagName === 'A' ? 80 : 0
      })
      vi.spyOn(HTMLElement.prototype, 'clientWidth', 'get').mockImplementation(function (this: HTMLElement) {
        return this.tagName === 'NAV' ? 100 : 0
      })
      const items = [
        { key: 'a', label: 'Alpha' },
        { key: 'b', label: 'Beta' },
        { key: 'c', label: 'Gamma' },
      ]
      const view = (active: string) => (
        <MemoryRouter>
          <Tabs items={items} active={active} hrefFor={(key) => `/${key}`} ariaLabel="Sections" />
        </MemoryRouter>
      )
      const { rerender } = render(view('b'))
      const strip = screen.getByRole('navigation', { name: 'Sections' })
      expect(strip.scrollLeft).toBe(290) // 300 - (100 - 80) / 2
      rerender(view('c'))
      expect(strip.scrollLeft).toBe(590)
    })
  })
})
