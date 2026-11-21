import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { installPhoneMedia } from '../../test/phone'
import { renderApp } from '../../test/renderApp'

let restoreMedia: () => void
beforeEach(() => {
  localStorage.clear()
  restoreMedia = installPhoneMedia(true)
})
afterEach(async () => {
  restoreMedia()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
  await new Promise((resolve) => setTimeout(resolve, 100))
})

const NODE = '/config?q=env%3ANODE_ENV'

describe('ConfigSearchPage on phones', () => {
  it('renders results as cards with match chips', async () => {
    renderApp(NODE)
    const list = await screen.findByRole('list', { name: 'Matching services' })
    expect(list).toHaveAttribute('data-layout', 'cards')
    expect(screen.queryByRole('table')).not.toBeInTheDocument()
    const cards = within(list).getAllByRole('listitem')
    expect(cards).toHaveLength(3)
    const payments = cards.find((card) => within(card).queryByRole('link', { name: 'payments-api' })) as HTMLElement
    expect(within(payments).getByText('env NODE_ENV')).toBeInTheDocument()
    expect(within(payments).getByText('core-services')).toBeInTheDocument()
    const notifications = cards.find((card) => within(card).queryByRole('link', { name: 'notifications' })) as HTMLElement
    expect(within(notifications).getByText('Previous revision :9 is still running')).toBeInTheDocument()
    // details are behind a disclosure
    expect(within(payments).queryByText(/DB_POOL_SIZE/)).not.toBeInTheDocument()
    await userEvent.setup().click(within(payments).getByRole('button', { name: 'Details' }))
    expect(within(payments).getByText(/DB_POOL_SIZE/)).toBeInTheDocument()
  })

  it('selects a card and exports it', async () => {
    const blobs: Blob[] = []
    vi.stubGlobal('URL', Object.assign(URL, { createObjectURL: (blob: Blob) => (blobs.push(blob), 'blob:test'), revokeObjectURL: () => {} }))
    vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})
    const user = userEvent.setup()
    renderApp(NODE)
    const list = await screen.findByRole('list', { name: 'Matching services' })
    await user.click(within(list).getByRole('checkbox', { name: 'Select notifications' }))
    expect(screen.getByText('1 selected')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Export CSV' }))
    expect(blobs).toHaveLength(1)
    const text = await new Promise<string>((resolve) => {
      const reader = new FileReader()
      reader.onload = () => resolve(String(reader.result))
      reader.readAsText(blobs[0])
    })
    expect(text).toContain('notifications,')
    expect(text).not.toContain('payments-api,')
  })
})
