import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { installPhoneMedia } from '../../../test/phone'
import { renderApp } from '../../../test/renderApp'
import { REVIEW_CUSTOM, REVIEW_SETTINGS_CUSTOM, useReviewMocks } from '../../../test/reviewSettingsFixtures'

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

  it('the instructions field fits a phone', async () => {
    useReviewMocks({ settings: REVIEW_SETTINGS_CUSTOM, reviews: [REVIEW_CUSTOM] })
    renderApp('/settings/agent')
    const panel = within(await screen.findByRole('region', { name: 'Reviews' }, SLOW))
    await userEvent.click(within((await panel.findByText('Team spend', undefined, SLOW)).closest('li') as HTMLElement).getByRole('button', { name: /^Edit/ }))
    const sheet = await screen.findByRole('dialog', { name: 'Review editor' })
    const field = within(sheet).getByRole('textbox', { name: 'Instructions' })
    expect(field).toHaveValue('Look at spend by team.\nKeep it short.')
    expect(within(sheet).getByText('37 of 2000 characters')).toBeInTheDocument()
    // One column in the sheet: the field is not a table cell and the card list has no table.
    expect(field.closest('table')).toBeNull()
  })
})
