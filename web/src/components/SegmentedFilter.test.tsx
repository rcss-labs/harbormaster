import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { Seg, SegmentedFilter } from './Seg'

const options = [
  { value: 'all', label: 'All', count: 12 },
  { value: 'problems', label: 'Problems', count: 3 },
]

describe('SegmentedFilter', () => {
  it('is a radiogroup with counts and a checked option', () => {
    render(<SegmentedFilter label="Status" options={options} value="problems" onChange={() => {}} />)
    expect(screen.getByRole('radiogroup', { name: 'Status' })).toBeInTheDocument()
    expect(screen.getByRole('radio', { name: /Problems/ })).toBeChecked()
    expect(screen.getByRole('radio', { name: /All/ })).not.toBeChecked()
    expect(screen.getByRole('radio', { name: 'Problems 3' })).toBeInTheDocument()
  })

  it('reports the chosen value', async () => {
    const onChange = vi.fn()
    render(<SegmentedFilter label="Status" options={options} value="all" onChange={onChange} />)
    await userEvent.click(screen.getByRole('radio', { name: /Problems/ }))
    expect(onChange).toHaveBeenCalledWith('problems')
  })

  it('moves selection with arrow keys', async () => {
    const onChange = vi.fn()
    render(<SegmentedFilter label="Status" options={options} value="all" onChange={onChange} />)
    screen.getByRole('radio', { name: /All/ }).focus()
    await userEvent.keyboard('{ArrowRight}')
    expect(onChange).toHaveBeenCalledWith('problems')
  })
})

describe('Seg (toggle semantics, unchanged)', () => {
  it('keeps aria-pressed buttons and shows counts', () => {
    render(<Seg label="Range" options={options} value="all" onChange={() => {}} />)
    expect(screen.getByRole('button', { name: /All/ })).toHaveAttribute('aria-pressed', 'true')
  })
})
