import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { installPhoneMedia } from '../../test/phone'
import { renderApp } from '../../test/renderApp'

let restoreMedia: () => void
beforeEach(() => {
  localStorage.clear()
  restoreMedia = installPhoneMedia(true)
})
afterEach(async () => {
  restoreMedia()
  await new Promise((resolve) => setTimeout(resolve, 100))
})

describe('SecurityPage on phones', () => {
  it('opens the detail in a drawer on a phone', async () => {
    const user = userEvent.setup()
    renderApp('/security')
    await user.click(await screen.findByText('platform-base:3.2.0'))
    const drawer = await screen.findByRole('dialog', { name: /platform-base:3\.2\.0/ })
    expect(await within(drawer).findByText('214 packages')).toBeInTheDocument()
    expect(screen.queryByRole('region', { name: 'Image detail' })).not.toBeInTheDocument()
    await user.click(within(drawer).getByRole('button', { name: 'Close panel' }))
    expect(screen.queryByRole('dialog', { name: /platform-base/ })).not.toBeInTheDocument()
    expect(screen.getByTestId('search')).not.toHaveTextContent('image=')
  })
})
