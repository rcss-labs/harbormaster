import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { Panel } from './Panel'

describe('Panel', () => {
  it('is a labelled region with title and body', () => {
    render(<Panel title="Needs attention">body text</Panel>)
    const region = screen.getByRole('region', { name: 'Needs attention' })
    expect(region).toHaveTextContent('body text')
    expect(screen.getByRole('heading', { name: 'Needs attention' })).toBeInTheDocument()
  })

  it('renders the toolbar slot next to the title', () => {
    render(
      <Panel title="Clusters" toolbar={<button type="button">Filter</button>}>
        x
      </Panel>,
    )
    expect(screen.getByRole('button', { name: 'Filter' })).toBeInTheDocument()
  })

  it('renders trailing actions in the header', () => {
    render(
      <Panel title="Tasks" actions={<a href="/tasks">Show all</a>}>
        x
      </Panel>,
    )
    expect(screen.getByRole('link', { name: 'Show all' })).toBeInTheDocument()
  })
})
