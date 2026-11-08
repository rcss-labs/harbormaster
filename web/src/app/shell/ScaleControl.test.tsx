import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { setScale } from '../scale'
import { ScaleControl } from './ScaleControl'

beforeEach(() => {
  localStorage.clear()
  setScale('auto')
})
afterEach(() => {
  setScale('auto')
  delete document.documentElement.dataset.scale
})

describe('ScaleControl', () => {
  it('segments, current value, note text', async () => {
    const user = userEvent.setup()
    render(<ScaleControl />)
    await user.click(screen.getByRole('button', { name: 'UI scale' }))
    const popover = screen.getByRole('group', { name: 'UI scale' })
    const choices = within(popover)
    for (const label of ['Auto', '100 %', '125 %', '150 %', '200 %']) {
      expect(choices.getByRole('button', { name: label })).toBeInTheDocument()
    }
    expect(choices.getByRole('button', { name: 'Auto' })).toHaveAttribute('aria-pressed', 'true')
    expect(choices.getByText('Auto uses 150 % on windows 3000 px and wider.')).toBeInTheDocument()
  })

  it('choosing a value remembers it and applies it', async () => {
    const user = userEvent.setup()
    render(<ScaleControl />)
    await user.click(screen.getByRole('button', { name: 'UI scale' }))
    await user.click(screen.getByRole('button', { name: '125 %' }))
    expect(localStorage.getItem('harbormaster-scale')).toBe('125')
    expect(document.documentElement.dataset.scale).toBe('125')
    expect(screen.getByRole('button', { name: '125 %' })).toHaveAttribute('aria-pressed', 'true')
  })
})
