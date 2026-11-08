import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { readFileSync } from 'node:fs'
import { useState } from 'react'
import { MemoryRouter } from 'react-router'
import { describe, expect, it } from 'vitest'
import { BottomSheet, SheetAction } from './BottomSheet'

function Host({ role, tall }: { role?: 'dialog' | 'menu'; tall?: boolean }) {
  const [open, setOpen] = useState(false)
  return (
    <MemoryRouter>
      <button type="button" onClick={() => setOpen(true)}>
        Open sheet
      </button>
      <BottomSheet open={open} onClose={() => setOpen(false)} label="More actions for payments-api" title="payments-api" sub="prod" role={role} tall={tall}>
        {role === 'menu' ? (
          <>
            <SheetAction label="Restart" onClick={() => {}} />
            <SheetAction label="Deploy" disabled why="Needs Deploy on Prod." />
            <SheetAction label="Logs" href="/logs" current />
          </>
        ) : (
          <button type="button">Inside</button>
        )}
      </BottomSheet>
    </MemoryRouter>
  )
}

describe('BottomSheet', () => {
  it('renders a portal dialog with the label, scrim closes, Esc closes, focus moves in and returns', async () => {
    const user = userEvent.setup()
    const { container } = render(<Host />)
    const opener = screen.getByRole('button', { name: 'Open sheet' })
    await user.click(opener)

    const dialog = screen.getByRole('dialog', { name: 'More actions for payments-api' })
    expect(dialog).toHaveAttribute('aria-modal', 'true')
    expect(container).not.toContainElement(dialog)
    expect(dialog.parentElement).toBe(document.body)
    expect(dialog).toHaveTextContent('payments-api')
    expect(dialog).toHaveTextContent('prod')
    expect(dialog).toContainElement(document.activeElement as HTMLElement)

    await user.click(screen.getByRole('button', { name: 'Close' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    expect(opener).toHaveFocus()

    await user.click(opener)
    expect(screen.getByRole('dialog')).toBeInTheDocument()
    await user.keyboard('{Escape}')
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    expect(opener).toHaveFocus()
  })

  it('popstate closes the sheet; closing pops its own entry only', async () => {
    const user = userEvent.setup()
    render(<Host />)
    await user.click(screen.getByRole('button', { name: 'Open sheet' }))
    expect(history.state).toMatchObject({ hmSheet: expect.any(String) })

    history.back()
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    expect(history.state?.hmSheet).toBeUndefined()

    // Opening and closing through the UI leaves no sheet entry behind.
    await user.click(screen.getByRole('button', { name: 'Open sheet' }))
    await user.click(screen.getByRole('button', { name: 'Close' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    await waitFor(() => expect(history.state?.hmSheet).toBeUndefined())
  })

  it('role=menu renders SheetAction rows as menuitems with the why line inside the row', async () => {
    const user = userEvent.setup()
    render(<Host role="menu" />)
    await user.click(screen.getByRole('button', { name: 'Open sheet' }))

    const menu = screen.getByRole('menu', { name: 'More actions for payments-api' })
    expect(menu).toBeInTheDocument()
    const items = screen.getAllByRole('menuitem')
    expect(items.map((item) => item.textContent)).toEqual(['Restart', 'DeployNeeds Deploy on Prod.', 'Logs'])
    const deploy = screen.getByRole('menuitem', { name: /Deploy/ })
    expect(deploy).toBeDisabled()
    expect(deploy).toHaveTextContent('Needs Deploy on Prod.')
    const link = screen.getByRole('menuitem', { name: 'Logs' })
    expect(link).toHaveAttribute('href', '/logs')
    expect(link).toHaveAttribute('aria-current', 'page')
  })

  it('tall sheet uses --hm-vvh', async () => {
    const user = userEvent.setup()
    render(<Host tall />)
    await user.click(screen.getByRole('button', { name: 'Open sheet' }))
    expect(screen.getByRole('dialog').className).toContain('tall')
    const css = readFileSync('src/components/BottomSheet.module.css', 'utf8')
    expect(css).toMatch(/\.tall\s*\{[^}]*var\(--hm-vvh, 100dvh\) - 24px/)
    expect(css).toMatch(/\.sheet\s*\{[^}]*max-height: calc\(var\(--hm-vvh, 100dvh\) - 48px\)/)
  })
})
