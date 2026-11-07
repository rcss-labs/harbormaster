import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { describe, expect, it } from 'vitest'
import { SERVICE_PAYMENTS } from '../../test/fixtures'
import { server } from '../../test/msw'
import { renderApp } from '../../test/renderApp'

describe('History tab (operations)', () => {
  it('shows a panel error with Retry instead of a blank, and recovers on Retry', async () => {
    let calls = 0
    server.use(
      http.get('/api/services/:id/operations', () => {
        calls += 1
        return calls === 1
          ? HttpResponse.json({ error: { code: 'internal', message: 'boom' } }, { status: 500 })
          : HttpResponse.json({ items: [], next: null })
      }),
    )
    renderApp(`/services/${SERVICE_PAYMENTS.id}/operations`)

    expect(await screen.findByRole('alert')).toHaveTextContent('The history could not be read.')
    await userEvent.click(screen.getByRole('button', { name: 'Retry' }))
    expect(await screen.findByText('No entries.')).toBeInTheDocument()
  })
})
