import { screen, within } from '@testing-library/react'
import { http, HttpResponse } from 'msw'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { ME_OMER } from '../../test/fixtures'
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

describe('MyAccess on a phone', () => {
  it('shows one panel per tag with every action marked granted or not', async () => {
    server.use(http.get('/api/me', () => HttpResponse.json(ME_OMER)))
    renderApp('/me/access')
    const dev = await screen.findByRole('region', { name: 'Permissions on Dev' })
    const marks = (panel: HTMLElement) =>
      within(panel)
        .getAllByRole('term')
        .map((term) => `${term.textContent}:${term.nextElementSibling?.textContent}`)
    expect(marks(dev).slice(0, 6)).toEqual(['View:✓', 'Logs:✓', 'Operate:✓', 'Deploy:–', 'Edit task definition:–', 'Terminal:–'])
    const staging = screen.getByRole('region', { name: 'Permissions on Staging' })
    expect(marks(staging).slice(0, 6)).toEqual(['View:✓', 'Logs:–', 'Operate:–', 'Deploy:–', 'Edit task definition:–', 'Terminal:–'])
  })

  it('keeps the admin note', async () => {
    renderApp('/me/access')
    expect(await screen.findByText('You are a global admin: you can do everything, on every tag.')).toBeInTheDocument()
  })
})
