import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
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
})
