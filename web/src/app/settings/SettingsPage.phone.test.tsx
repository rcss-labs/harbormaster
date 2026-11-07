import { fireEvent, screen, waitFor } from '@testing-library/react'
import { http, HttpResponse } from 'msw'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { server } from '../../test/msw'
import { installPhoneMedia } from '../../test/phone'
import { renderApp } from '../../test/renderApp'

let restoreMedia: () => void
beforeEach(() => {
  restoreMedia = installPhoneMedia(true)
})
afterEach(async () => {
  restoreMedia()
  await new Promise((resolve) => setTimeout(resolve, 50))
})

describe('SettingsPage on a phone', () => {
  it('General save bar: Save sits in a bar that marks the page with data-hm-bar and saves', async () => {
    const bodies: unknown[] = []
    server.use(
      http.put('/api/settings', async ({ request }) => {
        bodies.push(await request.json())
        return HttpResponse.json({ stopped_tasks_window_minutes: 90 })
      }),
    )
    renderApp('/settings/general')
    const input = await screen.findByLabelText('Show stopped tasks from the last X minutes')
    const save = screen.getByRole('button', { name: 'Save' })
    expect(save.closest('[data-hm-bar]')).not.toBeNull()
    await waitFor(() => expect(document.documentElement.hasAttribute('data-hm-bar')).toBe(true))
    await waitFor(() => expect(input).toHaveValue(10))
    fireEvent.change(input, { target: { value: '90' } })
    fireEvent.click(save)
    await waitFor(() => expect(bodies).toEqual([{ stopped_tasks_window_minutes: 90 }]))
    expect(await screen.findByText('Saved.')).toBeInTheDocument()
  })

  it('shows the range message for an invalid value and drops the bar marker on leaving', async () => {
    const view = renderApp('/settings/general')
    const input = await screen.findByLabelText('Show stopped tasks from the last X minutes')
    await waitFor(() => expect(input).toHaveValue(10))
    fireEvent.change(input, { target: { value: '0' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    expect(screen.getByRole('alert')).toHaveTextContent('Enter a number from 1 to 1440.')
    view.unmount()
    expect(document.documentElement.hasAttribute('data-hm-bar')).toBe(false)
  })

  it('other tabs keep their primary action in the header and no bar marker', async () => {
    renderApp('/settings/users')
    expect(await screen.findByRole('button', { name: 'Add user' })).toBeInTheDocument()
    expect(document.documentElement.hasAttribute('data-hm-bar')).toBe(false)
  })

  it('the settings heading stays for screen readers', async () => {
    renderApp('/settings/general')
    expect(await screen.findByRole('heading', { level: 1, name: 'Settings' })).toHaveClass('hm-phone-title-sr')
  })
})
