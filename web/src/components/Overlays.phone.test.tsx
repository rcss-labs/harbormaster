import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { MemoryRouter } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { installPhoneMedia } from '../test/phone'
import { Dialog } from './Dialog'
import { Drawer } from './Drawer'
import { Popover } from './Popover'
import { RowMenu, RowMenuButton, RowMenuLink } from './RowMenu'

let restoreMedia: () => void
beforeEach(() => {
  restoreMedia = installPhoneMedia(true)
})
afterEach(() => restoreMedia())

describe('Drawer on phones', () => {
  function Host() {
    const [open, setOpen] = useState(false)
    return (
      <div>
        <button type="button" onClick={() => setOpen(true)}>
          Open task
        </button>
        <Drawer open={open} onClose={() => setOpen(false)} title="Task 1a2b3c4d" sub="payments-api" footer={<button type="button">Stop task</button>}>
          <p>Drawer body</p>
        </Drawer>
      </div>
    )
  }

  it('renders as a tall bottom sheet on phones', async () => {
    const user = userEvent.setup()
    render(<Host />)
    await user.click(screen.getByRole('button', { name: 'Open task' }))
    const sheet = screen.getByRole('dialog', { name: 'Task 1a2b3c4d' })
    expect(sheet.className).toContain('tall')
    expect(sheet).toHaveTextContent('payments-api')
    expect(sheet).toHaveTextContent('Drawer body')
    expect(within(sheet).getByRole('button', { name: 'Stop task' })).toBeInTheDocument()
    expect(screen.queryByTestId('drawer-scrim')).not.toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: 'Close panel' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
  })
})

describe('Dialog on phones', () => {
  it('renders as a bottom sheet with full-width footer actions on phones, primary last', async () => {
    const user = userEvent.setup()
    const onSave = vi.fn()
    const onCancel = vi.fn()
    render(
      <Dialog
        title="Change desired count"
        sub="payments-api"
        onClose={onCancel}
        actions={[
          { label: 'Save', kind: 'primary', onClick: onSave },
          { label: 'Cancel', onClick: onCancel },
        ]}
      >
        <label>
          Count
          <input defaultValue="2" />
        </label>
      </Dialog>,
    )
    const sheet = screen.getByRole('dialog', { name: 'Change desired count' })
    expect(sheet.className).toContain('sheet')
    expect(sheet).toHaveTextContent('payments-api')
    const footer = sheet.querySelector('footer') as HTMLElement
    expect(within(footer).getAllByRole('button').map((b) => b.textContent)).toEqual(['Cancel', 'Save'])
    expect(screen.getByLabelText('Count')).toHaveFocus()

    await user.click(within(footer).getByRole('button', { name: 'Save' }))
    expect(onSave).toHaveBeenCalledOnce()
    await user.click(screen.getByTestId('sheet-scrim'))
    expect(onCancel).toHaveBeenCalled()
  })
})

describe('Popover on phones', () => {
  function Host({ inline }: { inline?: boolean }) {
    const [open, setOpen] = useState(false)
    return (
      <div>
        <button type="button" onClick={() => setOpen((v) => !v)}>
          Opener
        </button>
        <Popover open={open} onClose={() => setOpen(false)} label="Pick one" inline={inline}>
          <button type="button">Inside</button>
        </Popover>
      </div>
    )
  }

  it('renders as a bottom sheet titled with the label on phones', async () => {
    const user = userEvent.setup()
    const { container } = render(<Host />)
    await user.click(screen.getByRole('button', { name: 'Opener' }))
    const sheet = screen.getByRole('dialog', { name: 'Pick one' })
    expect(container).not.toContainElement(sheet)
    expect(within(sheet).getByRole('heading', { name: 'Pick one' })).toBeInTheDocument()
  })

  it('inline keeps the popover', async () => {
    const user = userEvent.setup()
    const { container } = render(<Host inline />)
    await user.click(screen.getByRole('button', { name: 'Opener' }))
    expect(container).toContainElement(screen.getByRole('dialog', { name: 'Pick one' }))
  })
})

describe('RowMenu on phones', () => {
  it('renders a role=menu sheet on phones; menuitems use the 52 px sheet class', async () => {
    const user = userEvent.setup()
    const onStop = vi.fn()
    render(
      <MemoryRouter>
        <RowMenu label="Actions for payments-api">
          <RowMenuLink to="/services/1">Open</RowMenuLink>
          <RowMenuButton danger onClick={onStop}>
            Stop
          </RowMenuButton>
        </RowMenu>
      </MemoryRouter>,
    )
    await user.click(screen.getByRole('button', { name: 'Actions for payments-api' }))
    const menu = screen.getByRole('menu', { name: 'Actions for payments-api' })
    expect(menu.className).toContain('sheet')
    const items = within(menu).getAllByRole('menuitem')
    expect(items.map((item) => item.textContent)).toEqual(['Open', 'Stop'])
    for (const item of items) expect(item.className).toContain('sheetItem')

    await user.click(within(menu).getByRole('menuitem', { name: 'Stop' }))
    expect(onStop).toHaveBeenCalledOnce()
    await waitFor(() => expect(screen.queryByRole('menu')).not.toBeInTheDocument())
  })
})
