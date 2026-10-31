import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { describe, expect, it } from 'vitest'
import { REVISIONS_PAYMENTS, SERVICE_NOTIFICATIONS, SERVICE_PAYMENTS } from '../../test/fixtures'
import { server } from '../../test/msw'
import { renderApp } from '../../test/renderApp'

describe('ServicePage banners', () => {
  it('shows the pending revision banner and a :N ready badge on the Task definition tab', async () => {
    renderApp(`/services/${SERVICE_PAYMENTS.id}/overview`)
    expect(await screen.findByText(/is registered but not deployed/)).toHaveTextContent('payments-api:43')
    expect(screen.getByRole('link', { name: /Task definition/ })).toHaveTextContent(':43 ready')
  })

  it('Deploy on the banner opens the deploy dialog on the Task definition tab', async () => {
    renderApp(`/services/${SERVICE_PAYMENTS.id}/overview`)
    await userEvent.click(await screen.findByRole('button', { name: 'Deploy :43' }))
    await waitFor(() => expect(screen.getByTestId('location')).toHaveTextContent(`/services/${SERVICE_PAYMENTS.id}/task-definition`))
    expect(await screen.findByRole('dialog')).toHaveTextContent('Deploy payments-api to :43?')
  })

  it('hides the pending banner while a rollout is running', async () => {
    server.use(http.get('/api/services/:id/revisions', () => HttpResponse.json({ ...REVISIONS_PAYMENTS, rolling_out_revision: 43 })))
    renderApp(`/services/${SERVICE_NOTIFICATIONS.id}/overview`)
    await screen.findByText('Rollout in progress')
    expect(screen.queryByText(/is registered but not deployed/)).not.toBeInTheDocument()
  })

  it('shows the controller banner on every tab when the controller is unsupported', async () => {
    server.use(
      http.get('/api/services/:id', () =>
        HttpResponse.json({ ...SERVICE_PAYMENTS, controller: { type: 'CODE_DEPLOY', supported: false, name: '', banner: 'This service uses CODE_DEPLOY.' } }),
      ),
    )
    renderApp(`/services/${SERVICE_PAYMENTS.id}/operations`)
    expect(await screen.findByText('This service uses CODE_DEPLOY.')).toBeInTheDocument()
  })
})
