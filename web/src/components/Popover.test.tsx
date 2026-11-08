import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { describe, expect, it } from 'vitest'
import { Popover } from './Popover'

function Host({ role }: { role?: 'dialog' | 'menu' | 'group' }) {
  const [open, setOpen] = useState(false)
  return (
    <div>
      <button type="button" onClick={() => setOpen((v) => !v)}>
        Opener
      </button>
      <Popover open={open} onClose={() => setOpen(false)} label="Pick one" role={role}>
        <button type="button">Inside</button>
      </Popover>
      <p>Elsewhere</p>
    </div>
  )
}

describe('Popover', () => {
  it('renders nothing while closed', () => {
    render(<Host />)
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })

  it('opens as a labelled dialog by default, or with the given role', async () => {
    const user = userEvent.setup()
    const { unmount } = render(<Host />)
    await user.click(screen.getByRole('button', { name: 'Opener' }))
    expect(screen.getByRole('dialog', { name: 'Pick one' })).toBeInTheDocument()
    unmount()
    render(<Host role="menu" />)
    await user.click(screen.getByRole('button', { name: 'Opener' }))
    expect(screen.getByRole('menu', { name: 'Pick one' })).toBeInTheDocument()
  })

  it('moves focus in on open and returns it to the opener on Escape', async () => {
    const user = userEvent.setup()
    render(<Host />)
    const opener = screen.getByRole('button', { name: 'Opener' })
    await user.click(opener)
    expect(screen.getByRole('dialog').contains(document.activeElement)).toBe(true)
    await user.keyboard('{Escape}')
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(opener).toHaveFocus()
  })

  it('closes on an outside click, but a click on the opener is left to the opener', async () => {
    const user = userEvent.setup()
    render(<Host />)
    const opener = screen.getByRole('button', { name: 'Opener' })
    await user.click(opener)
    await user.click(screen.getByText('Elsewhere'))
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    await user.click(opener)
    expect(screen.getByRole('dialog')).toBeInTheDocument()
    await user.click(opener)
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })

  it('stays open for clicks inside', async () => {
    const user = userEvent.setup()
    render(<Host />)
    await user.click(screen.getByRole('button', { name: 'Opener' }))
    await user.click(screen.getByRole('button', { name: 'Inside' }))
    expect(screen.getByRole('dialog')).toBeInTheDocument()
  })
})
