import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { installPhoneMedia } from '../../../test/phone'
import { renderApp } from '../../../test/renderApp'
import { useReviewMocks } from '../../../test/reviewFixtures'

let restoreMedia: () => void
beforeEach(() => {
  localStorage.clear()
  window.history.replaceState(null, '', '/')
  restoreMedia = installPhoneMedia(true)
})
afterEach(async () => {
  restoreMedia()
  await new Promise((resolve) => setTimeout(resolve, 50))
})

const SLOW = { timeout: 4000 }

describe('ReviewsSection on a phone', () => {
  it('opens the editor in a sheet', async () => {
    useReviewMocks()
    renderApp('/settings/agent')
    const panel = within(await screen.findByRole('region', { name: 'Reviews' }, SLOW))
    expect(screen.queryByLabelText('Review name')).not.toBeInTheDocument()
    await userEvent.click(within((await panel.findByText('Weekly cost', undefined, SLOW)).closest('li') as HTMLElement).getByRole('button', { name: /^Edit/ }))
    const sheet = await screen.findByRole('dialog', { name: 'Review editor' })
    expect(within(sheet).getByLabelText('Review name')).toHaveValue('Weekly cost')
    expect(within(sheet).getByRole('button', { name: 'Save review' })).toBeInTheDocument()
  })

  it('lists the reviews as cards', async () => {
    useReviewMocks()
    renderApp('/settings/agent')
    const list = await screen.findByRole('list', { name: 'Reviews' }, SLOW)
    expect(within(list).getAllByRole('listitem')).toHaveLength(2)
    expect(screen.queryByRole('columnheader', { name: 'Next run' })).not.toBeInTheDocument()
  })
})
