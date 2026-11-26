import { screen, within } from '@testing-library/react'
import { http, HttpResponse } from 'msw'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import type { MeDTO } from '../../api/types'
import { ME_ADMIN } from '../../test/fixtures'
import { server } from '../../test/msw'
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

  it('no entry with a server that does not send reviews_enabled', async () => {
    const older: Partial<MeDTO> = { ...ME_ADMIN, agent_enabled: true }
    delete older.reviews_enabled
    server.use(http.get('/api/me', () => HttpResponse.json(older)))
    renderApp('/')
    await screen.findByRole('link', { name: /^Security/ })
    expect(screen.queryByRole('link', { name: 'Reviews' })).not.toBeInTheDocument()
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
