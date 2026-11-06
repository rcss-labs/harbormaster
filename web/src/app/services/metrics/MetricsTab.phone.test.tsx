import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { SERVICE_PAYMENTS } from '../../../test/fixtures'
import { installPhoneMedia } from '../../../test/phone'
import { renderApp } from '../../../test/renderApp'

let restoreMedia: () => void
beforeEach(() => {
  restoreMedia = installPhoneMedia(true)
})
afterEach(() => restoreMedia())

describe('MetricsTab on phones', () => {
  it('Range is a chip group and choosing a chip updates ?range=', async () => {
    const user = userEvent.setup()
    renderApp(`/services/${SERVICE_PAYMENTS.id}/metrics`)
    const group = await screen.findByRole('group', { name: 'Range' })
    const chips = within(group).getAllByRole('button')
    expect(chips.map((chip) => chip.textContent)).toEqual(['1h', '6h', '24h', '72h'])
    expect(chips[2]).toHaveAttribute('aria-pressed', 'true')
    expect(chips[2]).toHaveAttribute('data-hm-hit', '36')
    await user.click(chips[1])
    expect(screen.getByTestId('search')).toHaveTextContent('range=6h')
  })
})
