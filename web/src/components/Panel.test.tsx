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

  it('renders the footer slot below the body', () => {
    render(
      <Panel title="Rollouts" footer={<span>source deployment_history</span>}>
        body text
      </Panel>,
    )
    const region = screen.getByRole('region', { name: 'Rollouts' })
    expect(region).toHaveTextContent('source deployment_history')
    expect(screen.getByText('body text').compareDocumentPosition(screen.getByText('source deployment_history'))).toBe(
      Node.DOCUMENT_POSITION_FOLLOWING,
    )
  })

  it('renders the badge before the title and the legend in the header', () => {
    render(
      <Panel title="Why rollouts failed" badge={<span>PROPOSED</span>} legend={<span>3 legend items</span>}>
        x
      </Panel>,
    )
    const heading = screen.getByRole('heading', { name: 'Why rollouts failed' })
    expect(screen.getByText('PROPOSED').compareDocumentPosition(heading)).toBe(Node.DOCUMENT_POSITION_FOLLOWING)
    expect(screen.getByText('3 legend items')).toBeInTheDocument()
  })

  it('proposed variant sets data-variant', () => {
    render(
      <Panel title="Proposed one" variant="proposed">
        x
      </Panel>,
    )
    expect(screen.getByRole('region', { name: 'Proposed one' })).toHaveAttribute('data-variant', 'proposed')
  })

  it('defaults to the default variant', () => {
    render(<Panel title="Plain">x</Panel>)
    expect(screen.getByRole('region', { name: 'Plain' })).toHaveAttribute('data-variant', 'default')
  })
})
