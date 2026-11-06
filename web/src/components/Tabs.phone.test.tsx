import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { installPhoneMedia } from '../test/phone'
import { Tabs } from './Tabs'

let restoreMedia: () => void
beforeEach(() => {
  restoreMedia = installPhoneMedia(true)
})
afterEach(() => restoreMedia())

const items = [
  { key: 'a', label: 'Overview' },
  { key: 'b', label: 'Logs', badge: 3, group: 'Configure' },
  { key: 'c', label: 'Deploy', locked: { action: 'Operate', tag: 'Prod' }, group: 'Configure' },
]

describe('Tabs on phones', () => {
  it('phone strip has an All sections button whose sheet lists every item, marks the current one and disables locked ones with the reason', async () => {
    const user = userEvent.setup()
    render(
      <MemoryRouter>
        <Tabs items={items} active="a" hrefFor={(key) => `/${key}`} ariaLabel="Service sections" />
      </MemoryRouter>,
    )
    // The strip itself is unchanged: every tab is still a link in the nav.
    const nav = screen.getByRole('navigation', { name: 'Service sections' })
    expect(within(nav).getAllByRole('link')).toHaveLength(3)

    await user.click(screen.getByRole('button', { name: 'All sections' }))
    const menu = screen.getByRole('menu', { name: 'All sections' })
    const rows = within(menu).getAllByRole('menuitem')
    expect(rows).toHaveLength(3)
    expect(rows[0]).toHaveAttribute('aria-current', 'page')
    expect(rows[1]).toHaveTextContent('Logs')
    expect(rows[1]).toHaveTextContent('3')
    expect(rows[1]).not.toBeDisabled()
    expect(rows[2]).toBeDisabled()
    expect(rows[2]).toHaveTextContent('You need Operate on Prod')

    await user.click(rows[1])
    await waitFor(() => expect(screen.queryByRole('menu')).not.toBeInTheDocument())
  })
})
