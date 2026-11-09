import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { LOG_SEARCH_RESULT } from '../../test/logSearchFixtures'
import { installPhoneMedia } from '../../test/phone'
import { server } from '../../test/msw'
import { renderApp } from '../../test/renderApp'

let restoreMedia: () => void
beforeEach(() => {
  localStorage.clear()
  restoreMedia = installPhoneMedia(true)
})
afterEach(async () => {
  restoreMedia()
  await new Promise((resolve) => setTimeout(resolve, 100))
})

describe('LogSearchPage on phones', () => {
  it('stacks the form and renders lines as cards', async () => {
    const user = userEvent.setup()
    server.use(http.post('/api/log-search', () => HttpResponse.json(LOG_SEARCH_RESULT)))
    renderApp('/logs?services=svc-payments,svc-catalog')
    const form = await screen.findByRole('form', { name: 'Log search' })
    expect(form).toHaveAttribute('data-layout', 'stacked')
    await user.click(within(form).getByRole('button', { name: 'Run query' }))

    const list = await screen.findByRole('list', { name: 'Matching lines' })
    expect(list).toHaveAttribute('data-layout', 'cards')
    const first = within(list).getAllByRole('link')[0]
    // Time and service on the first line, the message on the second.
    const d = new Date('2026-11-08T10:00:03.250Z')
    const two = (n: number) => String(n).padStart(2, '0')
    const clock = `${two(d.getHours())}:${two(d.getMinutes())}:${two(d.getSeconds())}.${String(d.getMilliseconds()).padStart(3, '0')}`
    expect(within(first).getByText(clock)).toBeInTheDocument()
    expect(within(first).getByTestId('line-meta')).toHaveTextContent(`${clock}payments`)
    expect(within(first).getByTestId('line-message')).toHaveTextContent('ERROR card declined')
    // The histogram scrolls in its own panel instead of widening the page.
    expect(screen.getByRole('img', { name: 'Hits over time' }).parentElement).toHaveAttribute('data-scroll', 'x')
  })

  it('opens saved queries in a sheet', async () => {
    const user = userEvent.setup()
    renderApp('/logs')
    expect(await screen.findByRole('button', { name: 'Run query' })).toBeInTheDocument()
    expect(screen.queryByRole('region', { name: 'Saved queries' })).not.toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Saved' }))
    const sheet = await screen.findByRole('dialog', { name: 'Saved queries' })
    expect(await within(sheet).findByText('Payment errors')).toBeInTheDocument()
    await user.click(within(sheet).getByRole('button', { name: 'Load Payment errors' }))
    expect(screen.queryByRole('dialog', { name: 'Saved queries' })).not.toBeInTheDocument()
    expect(screen.getByRole('textbox', { name: 'Query' })).toHaveValue('filter @message like /ERROR/')
  })
})
