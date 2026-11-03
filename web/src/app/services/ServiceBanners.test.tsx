import { screen, waitFor } from '@testing-library/react'
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

  it('hides the pending banner while the revisions response reports a rollout', async () => {
    server.use(http.get('/api/services/:id/revisions', () => HttpResponse.json({ ...REVISIONS_PAYMENTS, rolling_out_revision: 43 })))
    renderApp(`/services/${SERVICE_PAYMENTS.id}/overview`)
    await screen.findByText('Completed')
    await waitFor(() => expect(screen.getByRole('link', { name: /Task definition/ })).not.toHaveTextContent('ready'))
    expect(screen.queryByText(/is registered but not deployed/)).not.toBeInTheDocument()
  })

  it('hides the pending banner while a deployment is IN_PROGRESS', async () => {
    server.use(http.get('/api/services/:id/revisions', () => HttpResponse.json({ ...REVISIONS_PAYMENTS, rolling_out_revision: null })))
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
