import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import type { FocusDTO } from '../api/focus'
import { LiveRefresh, pillText } from './LiveRefresh'

const ago = (seconds: number) => new Date(Date.now() - seconds * 1000).toISOString()
const focus = (patch: Partial<FocusDTO>): FocusDTO => ({ state: 'live', interval_seconds: 5, refreshed_at: ago(2), message: '', ...patch })

describe('LiveRefresh', () => {
  it('shows Live, every 5 s', () => {
    render(<LiveRefresh polledAt={ago(30)} focus={focus({})} />)
    expect(screen.getByText('Live, every 5 s')).toBeInTheDocument()
  })

  it('shows the stretched interval', () => {
    render(<LiveRefresh polledAt={ago(30)} focus={focus({ interval_seconds: 10 })} />)
    expect(screen.getByText('Refreshing every 10 s')).toBeInTheDocument()
    expect(screen.queryByText(/Live, every/)).not.toBeInTheDocument()
  })

  it('shows Slowed by AWS throttling', () => {
    render(<LiveRefresh polledAt={ago(30)} focus={focus({ state: 'throttled', interval_seconds: 20 })} />)
    expect(screen.getByText('Slowed by AWS throttling')).toBeInTheDocument()
  })

  it('shows Normal pace (live limit reached) for both caps and puts the message in the title', () => {
    for (const state of ['capped', 'user_capped'] as const) {
      const { unmount } = render(
        <LiveRefresh polledAt={ago(30)} focus={focus({ state, refreshed_at: null, interval_seconds: 30, message: 'Close one to refresh this page live.' })} />,
      )
      expect(screen.getByText('Normal pace (live limit reached)')).toHaveAttribute('title', 'Close one to refresh this page live.')
      unmount()
    }
  })

  it('shows Live refresh unavailable', () => {
    render(<LiveRefresh polledAt={ago(30)} focus={focus({ state: 'unavailable', refreshed_at: null, message: 'Live refresh unavailable' })} />)
    expect(screen.getByText('Live refresh unavailable')).toBeInTheDocument()
  })

  it('shows no pill before the first focus read', () => {
    const { container } = render(<LiveRefresh polledAt={ago(30)} focus={focus({ refreshed_at: null })} />)
    expect(screen.getByText(/^Updated/)).toBeInTheDocument()
    expect(container.querySelector('[aria-live="polite"]')?.textContent ?? '').toBe('')
  })

  it('uses the newer of the poll time and the focus read for the age', () => {
    render(<LiveRefresh polledAt={ago(300)} focus={focus({ refreshed_at: ago(10) })} />)
    expect(screen.getByText(/^Updated/).textContent).toMatch(/Updated 1\ds ago/)
  })

  it('uses the poll time when it is newer than the focus read', () => {
    render(<LiveRefresh polledAt={ago(20)} focus={focus({ refreshed_at: ago(300) })} />)
    expect(screen.getByText(/^Updated/).textContent).toMatch(/Updated 2\ds ago/)
  })

  it('shows the age alone when there is no focus state yet', () => {
    render(<LiveRefresh polledAt={ago(30)} />)
    expect(screen.getByText(/^Updated/).textContent).toMatch(/Updated 3\ds ago/)
  })

  it('renders nothing without an age or a pill', () => {
    const { container } = render(<LiveRefresh />)
    expect(container).toBeEmptyDOMElement()
  })

  it('announces a state change politely', () => {
    const { container } = render(<LiveRefresh polledAt={ago(30)} focus={focus({})} />)
    expect(container.querySelector('[aria-live="polite"]')).toHaveTextContent('Live, every 5 s')
  })
})

describe('pillText', () => {
  it('maps every state', () => {
    expect(pillText(focus({}))).toBe('Live, every 5 s')
    expect(pillText(focus({ interval_seconds: 6 }))).toBe('Refreshing every 6 s')
    expect(pillText(focus({ refreshed_at: null }))).toBeNull()
    expect(pillText(focus({ state: 'throttled' }))).toBe('Slowed by AWS throttling')
    expect(pillText(focus({ state: 'capped' }))).toBe('Normal pace (live limit reached)')
    expect(pillText(focus({ state: 'unavailable' }))).toBe('Live refresh unavailable')
  })
})
