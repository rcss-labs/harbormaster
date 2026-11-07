import { screen, waitFor } from '@testing-library/react'
import { http, HttpResponse } from 'msw'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { SERVICE_PAYMENTS } from '../../../test/fixtures'
import { server } from '../../../test/msw'
import { installPhoneMedia } from '../../../test/phone'
import { renderApp } from '../../../test/renderApp'
import { TIMELINE_FAILING } from '../../../test/timelineFixtures'

// Two changes 4% of the one hour range apart: 144 seconds.
function serveTwoCloseChanges() {
  const change = (id: string, at: string) => ({ ...TIMELINE_FAILING.events[0], id, title: `${id} change`, at, diff: [] })
  server.use(
    http.get('/api/services/:id/timeline', () =>
      HttpResponse.json({ ...TIMELINE_FAILING, incident: null, events: [change('Alpha', '2026-11-07T11:20:00Z'), change('Beta', '2026-11-07T11:22:24Z')] }),
    ),
  )
}
const markers = () => screen.getAllByRole('button', { name: /^\d\d:\d\d (Alpha|Beta) change$/ })

let restoreMedia: () => void
beforeEach(() => {
  restoreMedia = installPhoneMedia(true)
})
afterEach(() => restoreMedia())

describe('TimelineTab on phones', () => {
  it('the track scrolls inside its panel and the toggles wrap (class assertions)', async () => {
    renderApp(`/services/${SERVICE_PAYMENTS.id}/timeline`)

    const toggle = await screen.findByRole('checkbox', { name: 'Changes' })
    expect(toggle.closest('[data-testid="timeline-toggles"]')?.className).toContain('toggles')
    const track = await screen.findByTestId('timeline-track')
    expect(track.parentElement?.className).toContain('scroll')
    expect(track.className).toContain('track')
    expect(screen.getByTestId('timeline-split').className).toContain('stack')
  })

  it('merges markers closer than a 44px marker into one group', async () => {
    serveTwoCloseChanges()
    renderApp(`/services/${SERVICE_PAYMENTS.id}/timeline`)

    await waitFor(() => expect(markers()).toHaveLength(1))
    expect(markers()[0]).toHaveTextContent('2')
  })
})
