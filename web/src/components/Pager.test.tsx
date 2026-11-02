import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { Pager } from './Pager'

describe('Pager', () => {
  it('shows the visible range and total', () => {
    render(<Pager page={2} pageSize={25} total={60} onPageChange={() => {}} />)
    expect(screen.getByRole('navigation', { name: 'Pagination' })).toHaveTextContent('26–50 of 60')
  })

  it('moves to the next and previous page', async () => {
    const onPageChange = vi.fn()
    render(<Pager page={2} pageSize={25} total={60} onPageChange={onPageChange} />)
    await userEvent.click(screen.getByRole('button', { name: 'Next page' }))
    expect(onPageChange).toHaveBeenLastCalledWith(3)
    await userEvent.click(screen.getByRole('button', { name: 'Previous page' }))
    expect(onPageChange).toHaveBeenLastCalledWith(1)
  })

  it('disables the edges and clamps the last range', () => {
    const { rerender } = render(<Pager page={1} pageSize={25} total={60} onPageChange={() => {}} />)
    expect(screen.getByRole('button', { name: 'Previous page' })).toBeDisabled()
    rerender(<Pager page={3} pageSize={25} total={60} onPageChange={() => {}} />)
    expect(screen.getByRole('button', { name: 'Next page' })).toBeDisabled()
    expect(screen.getByRole('navigation')).toHaveTextContent('51–60 of 60')
  })

  it('renders nothing when everything fits on one page', () => {
    const { container } = render(<Pager page={1} pageSize={25} total={10} onPageChange={() => {}} />)
    expect(container).toBeEmptyDOMElement()
  })
})
