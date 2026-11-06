import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { describe, expect, it } from 'vitest'
import { Drawer } from './Drawer'

function Host({ wide }: { wide?: boolean }) {
  const [open, setOpen] = useState(false)
  return (
    <div>
      <button type="button" onClick={() => setOpen(true)}>
        Open task
      </button>
      <Drawer open={open} onClose={() => setOpen(false)} title="Task 1a2b3c4d" sub="payments-api" wide={wide} footer={<button type="button">Stop task</button>}>
        <p>Drawer body</p>
      </Drawer>
    </div>
  )
}

describe('Drawer', () => {
  it('renders nothing while closed', () => {
    render(<Host />)
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })

  it('is a modal dialog named by its title, with sub, body and footer', async () => {
    render(<Host />)
    await userEvent.click(screen.getByRole('button', { name: 'Open task' }))
    const dialog = screen.getByRole('dialog', { name: 'Task 1a2b3c4d' })
    expect(dialog).toHaveAttribute('aria-modal', 'true')
    expect(dialog).toHaveTextContent('payments-api')
    expect(dialog).toHaveTextContent('Drawer body')
    expect(screen.getByRole('button', { name: 'Stop task' })).toBeInTheDocument()
  })

  it('moves focus in and returns it to the opener on Escape', async () => {
    const user = userEvent.setup()
    render(<Host />)
    const opener = screen.getByRole('button', { name: 'Open task' })
    await user.click(opener)
    expect(screen.getByRole('dialog').contains(document.activeElement)).toBe(true)
    await user.keyboard('{Escape}')
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(opener).toHaveFocus()
  })

  it('closes from the scrim and from the close button', async () => {
    const user = userEvent.setup()
    render(<Host />)
    await user.click(screen.getByRole('button', { name: 'Open task' }))
    await user.click(screen.getByRole('button', { name: 'Close panel' }))
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Open task' }))
    await user.click(screen.getByTestId('drawer-scrim'))
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })

  it('keeps Tab inside the drawer', async () => {
    const user = userEvent.setup()
    render(<Host />)
    await user.click(screen.getByRole('button', { name: 'Open task' }))
    const dialog = screen.getByRole('dialog')
    for (let i = 0; i < 5; i += 1) {
      await user.tab()
      expect(dialog.contains(document.activeElement)).toBe(true)
    }
  })
})
