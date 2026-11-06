import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
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

  it('renders the legend in the header', () => {
    render(
      <Panel title="Why rollouts failed" legend={<span>3 legend items</span>}>
        x
      </Panel>,
    )
    expect(screen.getByText('3 legend items')).toBeInTheDocument()
  })

  it('has a single look: no variant attribute', () => {
    render(<Panel title="Plain">x</Panel>)
    expect(screen.getByRole('region', { name: 'Plain' })).not.toHaveAttribute('data-variant')
  })

  it('label sets aria-label on the section', () => {
    render(
      <Panel title="Clusters" label="Cluster list">
        x
      </Panel>,
    )
    expect(screen.getByRole('region', { name: 'Cluster list' })).toBeInTheDocument()
  })

  it('error shows the message in place of children and Retry calls onRetry', async () => {
    const onRetry = vi.fn()
    render(
      <Panel title="Tasks" error={{ message: 'Tasks could not be loaded.', detail: 'AccessDenied: ecs:ListTasks', onRetry }}>
        body text
      </Panel>,
    )
    expect(screen.getByRole('alert')).toHaveTextContent('Tasks could not be loaded.')
    expect(screen.queryByText('body text')).not.toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'Retry' }))
    expect(onRetry).toHaveBeenCalledTimes(1)
  })

  it('keeps the error detail behind a Show error disclosure', () => {
    render(<Panel title="Tasks" error={{ message: 'Nope.', detail: 'AccessDenied: ecs:ListTasks' }}>x</Panel>)
    const summary = screen.getByText('Show error')
    expect(summary.closest('details')).not.toHaveAttribute('open')
    expect(screen.getByText('AccessDenied: ecs:ListTasks').closest('details')).toBe(summary.closest('details'))
    expect(screen.queryByRole('button', { name: 'Retry' })).not.toBeInTheDocument()
  })

  it('loading renders a status skeleton instead of children', () => {
    render(<Panel title="Tasks" loading>body text</Panel>)
    expect(screen.getByRole('status')).toBeInTheDocument()
    expect(screen.queryByText('body text')).not.toBeInTheDocument()
  })
})
