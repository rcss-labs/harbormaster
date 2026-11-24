import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { installPhoneMedia } from '../../test/phone'
import { renderApp } from '../../test/renderApp'
import { mockAgent } from './agentTestKit'

let restoreMedia: () => void
beforeEach(() => {
  restoreMedia = installPhoneMedia(true)
  localStorage.clear()
})
afterEach(async () => {
  restoreMedia()
  await new Promise((resolve) => setTimeout(resolve, 100))
})

describe('AgentDrawer on phones', () => {
  it('opens as a bottom sheet', async () => {
    mockAgent()
    const user = userEvent.setup()
    renderApp('/clusters')
    await user.click(await screen.findByRole('button', { name: 'Ask AI' }))
    const sheet = await screen.findByRole('dialog', { name: 'Agent' })
    expect(sheet.className).toContain('tall')
    expect(screen.queryByTestId('drawer-scrim')).not.toBeInTheDocument()
  })
})
