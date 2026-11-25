import { screen, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { installPhoneMedia } from '../../test/phone'
import { mockReviews } from '../../test/reviewFixtures'
import { renderApp } from '../../test/renderApp'

describe('Reviews navigation', () => {
  it('entry present only when reviews are enabled', async () => {
    mockReviews([], true)
    const { unmount } = renderApp('/')
    const link = await screen.findByRole('link', { name: 'Reviews' })
    expect(link).toHaveAttribute('href', '/reviews')
    unmount()

    for (const agentEnabled of [false, true]) {
      mockReviews([], false, 50, agentEnabled)
      const off = renderApp('/')
      await screen.findByRole('link', { name: /^Security/ })
      expect(screen.queryByRole('link', { name: 'Reviews' })).not.toBeInTheDocument()
      off.unmount()
    }
  })

  describe('on a phone', () => {
    let restoreMedia: () => void
    beforeEach(() => {
      restoreMedia = installPhoneMedia(true)
    })
    afterEach(() => restoreMedia())

    it('More lists Reviews only when reviews are enabled', async () => {
      mockReviews([], true)
      const { unmount } = renderApp('/more')
      const insights = within(await screen.findByRole('region', { name: 'Insights' }))
      expect(await insights.findByRole('link', { name: /^Reviews/ })).toHaveAttribute('href', '/reviews')
      unmount()

      for (const agentEnabled of [false, true]) {
        mockReviews([], false, 50, agentEnabled)
        const off = renderApp('/more')
        const again = within(await screen.findByRole('region', { name: 'Insights' }))
        expect(again.getByRole('link', { name: 'Log search' })).toBeInTheDocument()
        expect(again.queryByRole('link', { name: /^Reviews/ })).not.toBeInTheDocument()
        off.unmount()
      }
    })
  })
})
