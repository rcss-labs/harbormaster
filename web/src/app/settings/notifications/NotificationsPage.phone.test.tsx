import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { installPhoneMedia } from '../../../test/phone'
import { renderApp } from '../../../test/renderApp'

const css = readFileSync(resolve(process.cwd(), 'src/app/settings/notifications/notifications.module.css'), 'utf8')

let restoreMedia: () => void
beforeEach(() => {
  restoreMedia = installPhoneMedia(true)
})
afterEach(async () => {
  restoreMedia()
  await new Promise((resolve) => setTimeout(resolve, 50))
})

describe('NotificationsPage on a phone', () => {
  it('opens the editor in a sheet', async () => {
    renderApp('/settings/notifications')
    const list = await screen.findByRole('list', { name: 'Routes' })
    expect(screen.queryByLabelText('Route name')).not.toBeInTheDocument()
    await userEvent.click(within(list).getByRole('button', { name: /core-services to #core-alerts/ }))
    const sheet = await screen.findByRole('dialog', { name: 'Route editor' })
    expect(within(sheet).getByLabelText('Route name')).toHaveValue('core-services to #core-alerts')
    expect(within(sheet).getByRole('button', { name: 'Save route' })).toBeInTheDocument()
    // The fields stack in one column; the phone block of the stylesheet is where that is defined.
    const phoneBlock = css.slice(css.indexOf('@media (max-width: 700px)'))
    expect(phoneBlock).toMatch(/\.fields\s*{\s*grid-template-columns:\s*minmax\(0,\s*1fr\)/)
  })

  it('renders the log as cards', async () => {
    renderApp('/settings/notifications?view=log')
    const cards = await screen.findByRole('list', { name: 'Deliveries' })
    expect(within(cards).getAllByRole('listitem').length).toBeGreaterThan(3)
    expect(screen.queryByRole('table')).not.toBeInTheDocument()
    expect(screen.queryByRole('columnheader')).not.toBeInTheDocument()
    expect(within(cards).getByText('image-resizer: restart loop')).toBeInTheDocument()
    expect(within(cards).getByRole('button', { name: 'Retry' })).toBeInTheDocument()
  })

  it('keeps the threshold input usable inside a wrapped chip', async () => {
    renderApp('/settings/notifications')
    const list = await screen.findByRole('list', { name: 'Routes' })
    await userEvent.click(within(list).getByRole('button', { name: /core-services to #core-alerts/ }))
    const sheet = await screen.findByRole('dialog', { name: 'Route editor' })
    const input = within(sheet).getByRole('spinbutton', { name: 'Failing services needed for core-services' })
    expect(input.closest('[role="listitem"]')).not.toBeNull()
    await userEvent.clear(input)
    await userEvent.type(input, '6')
    expect(input).toHaveValue(6)
    // jsdom has no layout: the rules that wrap the chip and keep it inside the sheet are read from the stylesheet.
    expect(css).toMatch(/\.chip\s*{[^}]*flex-wrap:\s*wrap/)
    expect(css).toMatch(/\.chip\s*{[^}]*max-width:\s*100%/)
  })
})
