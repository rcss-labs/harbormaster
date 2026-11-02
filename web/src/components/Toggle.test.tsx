import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { Toggle } from './Toggle'

describe('Toggle', () => {
  it('is a checkbox named by its label and reports the new value', async () => {
    const onChange = vi.fn()
    render(<Toggle checked={false} onChange={onChange}>Group repeats</Toggle>)
    const box = screen.getByRole('checkbox', { name: 'Group repeats' })
    expect(box).not.toBeChecked()
    await userEvent.click(box)
    expect(onChange).toHaveBeenCalledWith(true)
  })

  it('reflects checked and ignores clicks when disabled', async () => {
    const onChange = vi.fn()
    render(<Toggle checked disabled onChange={onChange}>Problems only</Toggle>)
    const box = screen.getByRole('checkbox', { name: 'Problems only' })
    expect(box).toBeChecked()
    await userEvent.click(box)
    expect(onChange).not.toHaveBeenCalled()
  })
})
