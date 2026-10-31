import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { ReviewDialog } from './ReviewDialog'

const base = {
  serviceName: 'api',
  deployedRevision: 42,
  predictedRevision: 43,
  baseRevision: 42,
  before: '{\n  "cpu": "256"\n}',
  after: '{\n  "cpu": "512"\n}',
  registering: false,
  onKeepEditing: () => {},
  onRegister: () => {},
}

describe('ReviewDialog', () => {
  it('renders title, sub and diff rows', () => {
    render(<ReviewDialog {...base} />)
    expect(screen.getByRole('heading', { name: 'Register api:43?' })).toBeInTheDocument()
    expect(screen.getByText('Changes against the deployed revision :42.')).toBeInTheDocument()
    expect(screen.getByText(/"cpu": "512"/)).toBeInTheDocument()
    expect(screen.queryByText(/while you were editing/)).not.toBeInTheDocument()
  })

  it('warns only when the base revision differs from the deployed one', () => {
    render(<ReviewDialog {...base} baseRevision={41} />)
    expect(
      screen.getByText('The service moved from :41 to :42 while you were editing. Check the diff before saving.'),
    ).toBeInTheDocument()
  })

  it('calls onRegister and disables while registering', async () => {
    const onRegister = vi.fn()
    const { rerender } = render(<ReviewDialog {...base} onRegister={onRegister} />)
    await userEvent.click(screen.getByRole('button', { name: 'Register revision :43' }))
    expect(onRegister).toHaveBeenCalledTimes(1)
    rerender(<ReviewDialog {...base} registering onRegister={onRegister} />)
    expect(screen.getByRole('button', { name: 'Register revision :43' })).toBeDisabled()
  })

  it('shows an error', () => {
    render(<ReviewDialog {...base} error="boom" />)
    expect(screen.getByText('boom')).toBeInTheDocument()
  })
})
