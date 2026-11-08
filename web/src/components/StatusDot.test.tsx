import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import type { Status } from '../api/types'
import { StatusDot } from './StatusDot'

describe('StatusDot', () => {
  it('labels a service status from the shared status map', () => {
    render(<StatusDot status="unhealthy_task" />)
    expect(screen.getByText('Unhealthy task')).toBeInTheDocument()
  })

  it('labels a cluster status', () => {
    render(<StatusDot clusterStatus="needs_attention" />)
    expect(screen.getByText('Needs attention')).toBeInTheDocument()
  })

  it('shows custom text for an explicit kind', () => {
    render(<StatusDot kind="bad">Stopped</StatusDot>)
    expect(screen.getByText('Stopped')).toBeInTheDocument()
  })

  it('treats an empty service status as a dash', () => {
    render(<StatusDot status="" />)
    expect(screen.getByText('–')).toBeInTheDocument()
  })

  it.each<[Status, string]>([
    ['steady', 'st-ok'],
    ['degraded', 'st-warn'],
    ['failing', 'st-bad'],
    ['deploying', 'st-info'],
    ['scaled_to_zero', 'st-mute'],
  ])('status %s gets the %s shape class', (status, expectedClass) => {
    const { container } = render(<StatusDot status={status} />)
    expect(container.firstElementChild).toHaveClass('st', expectedClass)
  })

  it('maps the expected kind to st-exp', () => {
    const { container } = render(<StatusDot kind="expected">Spot interruption</StatusDot>)
    expect(container.firstElementChild).toHaveClass('st', 'st-exp')
  })

  it('bare omits the text but keeps an accessible name', () => {
    render(<StatusDot status="failing" bare />)
    expect(screen.queryByText('Failing')).not.toBeInTheDocument()
    expect(screen.getByLabelText('Failing')).toBeInTheDocument()
  })

  it('shows shape, colour class and text together by default', () => {
    const { container } = render(<StatusDot status="failing" />)
    expect(container.firstElementChild).toHaveClass('st-bad')
    expect(container.firstElementChild).toHaveTextContent('Failing')
    expect(container.firstElementChild).not.toHaveClass('bare')
  })
})
