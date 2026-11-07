import { screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { SERVICE_PAYMENTS } from '../../../test/fixtures'
import { installPhoneMedia } from '../../../test/phone'
import { renderApp } from '../../../test/renderApp'

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
})
