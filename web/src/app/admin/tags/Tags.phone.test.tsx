import { fireEvent, screen, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { installPhoneMedia } from '../../../test/phone'
import { renderApp } from '../../../test/renderApp'

let restoreMedia: () => void
beforeEach(() => {
  restoreMedia = installPhoneMedia(true)
})
afterEach(async () => {
  restoreMedia()
  await new Promise((resolve) => setTimeout(resolve, 50))
})

describe('Tags on a phone', () => {
  it('lists tags as rows and keeps the users with access', async () => {
    renderApp('/settings/tags')
    const rows = await screen.findAllByRole('row')
    const prod = rows.find((row) => within(row).queryByText('Prod'))
    expect(prod).toBeDefined()
    expect(within(prod!).getByText(/dana/)).toBeInTheDocument()
    const dev = rows.find((row) => within(row).queryByText('default'))
    expect(dev).toBeDefined()
  })

  it('Delete is disabled for Dev with the reason inside the row, Edit stays available', async () => {
    renderApp('/settings/tags')
    fireEvent.click(await screen.findByRole('button', { name: 'Actions for tag Dev' }))
    const menu = screen.getByRole('menu', { name: 'Actions for tag Dev' })
    expect(within(menu).getByRole('menuitem', { name: /^Edit/ })).toBeEnabled()
    const remove = within(menu).getByRole('menuitem', { name: /^Delete/ })
    expect(remove).toBeDisabled()
    expect(remove).toHaveTextContent('Dev is the default tag')
  })

  it('Edit opens the dialog as a sheet', async () => {
    renderApp('/settings/tags')
    fireEvent.click(await screen.findByRole('button', { name: 'Actions for tag Prod' }))
    fireEvent.click(within(screen.getByRole('menu')).getByRole('menuitem', { name: 'Edit' }))
    expect(await screen.findByRole('dialog', { name: 'Edit tag Prod' })).toBeInTheDocument()
  })

  it('assignments stay editable: expanding a cluster shows its services with tag selects', async () => {
    renderApp('/settings/tags')
    const first = (await screen.findAllByRole('button', { expanded: false }))[0]
    fireEvent.click(first)
    expect((await screen.findAllByLabelText(/^Tag for /)).length).toBeGreaterThan(1)
  })
})
