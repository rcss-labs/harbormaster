import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { Chips } from './Chips'

const options = [
  { value: 'all', label: 'All', count: 12 },
  { value: 'problems', label: 'Problems', count: 3 },
  { value: 'locked', label: 'Locked' },
] as const

describe('Chips', () => {
  it('is a labelled group of aria-pressed chips with counts', () => {
    render(<Chips label="Saved views" options={[...options]} value="problems" onChange={() => {}} />)
    const group = screen.getByRole('group', { name: 'Saved views' })
    const chips = screen.getAllByRole('button')
    expect(group).toContainElement(chips[0])
    expect(chips.map((chip) => chip.getAttribute('aria-pressed'))).toEqual(['false', 'true', 'false'])
    expect(screen.getByRole('button', { name: 'Problems 3' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Locked' })).toBeInTheDocument()
  })

  it('reports the picked value', async () => {
    const onChange = vi.fn()
    render(<Chips label="Status" options={[...options]} value="all" onChange={onChange} />)
    await userEvent.click(screen.getByRole('button', { name: 'Locked' }))
    expect(onChange).toHaveBeenCalledWith('locked')
  })

  it('marks each chip as a 36 px target kept on one line', () => {
    render(<Chips label="Status" options={[...options]} value="all" onChange={() => {}} />)
    for (const chip of screen.getAllByRole('button')) {
      expect(chip).toHaveAttribute('data-hm-hit', '36')
      expect(chip).toHaveAttribute('data-hm-oneline')
    }
  })

  it('wraps instead of scrolling when asked', () => {
    const { rerender } = render(<Chips label="Status" options={[...options]} value="all" onChange={() => {}} />)
    expect(screen.getByRole('group')).not.toHaveClass('wrap')
    rerender(<Chips label="Status" options={[...options]} value="all" onChange={() => {}} wrap />)
    expect(screen.getByRole('group')).toHaveClass('wrap')
  })
})
