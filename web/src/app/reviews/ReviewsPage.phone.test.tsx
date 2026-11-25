import { screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { installPhoneMedia } from '../../test/phone'
import { REVIEW_RUN_DONE, mockReviews } from '../../test/reviewFixtures'
import { renderApp } from '../../test/renderApp'

let restoreMedia: () => void
beforeEach(() => {
  restoreMedia = installPhoneMedia(true)
})
afterEach(() => restoreMedia())

describe('Reviews on phones', () => {
  it('list and result fit a phone', async () => {
    mockReviews()
    const { container, unmount } = renderApp('/reviews')

    expect(await screen.findByRole('link', { name: 'Weekly <b>cost</b> check' })).toBeInTheDocument()
    // One column, no table, nothing that forces a sideways scroll.
    expect(container.querySelector('table')).toBeNull()
    expect(container.querySelector('[data-review-row]')?.className).toMatch(/row/)
    unmount()

    const detail = renderApp(`/reviews/runs/${REVIEW_RUN_DONE.id}`)
    expect(await screen.findByText('12 services in scope')).toBeInTheDocument()
    expect(detail.container.querySelector('table')).toBeNull()
    expect(screen.getByText('payments: 2 -> 4 tasks').tagName).toBe('PRE')
  })
})
