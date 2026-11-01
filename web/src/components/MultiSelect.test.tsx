import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { MultiSelect } from './MultiSelect'

const options = [
  { value: '2', label: 'Staging' },
  { value: '3', label: 'Prod' },
]

const setup = (value: string[] = [], onChange = vi.fn()) => {
  render(<MultiSelect label="Tag filter" placeholder="All tags" options={options} value={value} onChange={onChange} />)
  return onChange
}

describe('MultiSelect', () => {
  it('shows the placeholder when nothing is selected', () => {
    setup()
    expect(screen.getByRole('button', { name: 'Tag filter' })).toHaveTextContent('All tags')
  })

  it('lists options as checkboxes and reports toggles', async () => {
    const onChange = setup()
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'Tag filter' }))
    expect(screen.getByRole('group', { name: 'Tag filter options' })).toBeInTheDocument()
    await user.click(screen.getByRole('checkbox', { name: 'Prod' }))
    expect(onChange).toHaveBeenCalledWith(['3'])
  })

  it('button text lists the selected labels', () => {
    setup(['2', '3'])
    expect(screen.getByRole('button', { name: 'Tag filter' })).toHaveTextContent('Staging, Prod')
  })

  it('Escape closes the popover', async () => {
    setup()
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'Tag filter' }))
    await user.keyboard('{Escape}')
    expect(screen.queryByRole('group', { name: 'Tag filter options' })).not.toBeInTheDocument()
  })
})
