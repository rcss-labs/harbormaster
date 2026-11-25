import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { SERVICE_PAYMENTS } from '../../test/fixtures'
import { mockInvestigations } from '../../test/investigationFixtures'
import { installPhoneMedia } from '../../test/phone'
import { renderApp } from '../../test/renderApp'

let restoreMedia: () => void
beforeEach(() => {
  restoreMedia = installPhoneMedia(true)
})
afterEach(() => restoreMedia())

describe('InvestigationDrawer on phones', () => {
  it('opens as a bottom sheet', async () => {
    mockInvestigations()
    const user = userEvent.setup()
    renderApp(`/services/${SERVICE_PAYMENTS.id}/timeline`)

    await user.click(await screen.findByRole('button', { name: /Diagnosis ready/ }))

    const sheet = await screen.findByRole('dialog', { name: 'Investigation' })
    expect(sheet.className).toContain('tall')
    expect(screen.queryByTestId('drawer-scrim')).not.toBeInTheDocument()
    expect(await screen.findByText('CannotPullContainerError: manifest unknown')).toBeInTheDocument()
  })
})
