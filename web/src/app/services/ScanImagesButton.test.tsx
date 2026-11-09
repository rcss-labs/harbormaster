import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { describe, expect, it } from 'vitest'
import { SERVICE_PAYMENTS } from '../../test/fixtures'
import { server } from '../../test/msw'
import { renderApp } from '../../test/renderApp'
import { SCAN_STATUS_SCANNED, SECURITY_SERVICE_EDGE, SECURITY_SERVICE_OFF } from '../../test/securityFixtures'

const route = `/services/${SERVICE_PAYMENTS.id}`
const serveSetting = (setting: typeof SECURITY_SERVICE_EDGE) => server.use(http.get('/api/security/services/:serviceId', () => HttpResponse.json(setting)))
const header = async () => (await screen.findByRole('heading', { name: 'payments-api', level: 1 })).closest('div[class]')!.parentElement!.parentElement!

describe('Scan image button in the service header', () => {
  it('sits left of Deploy and scans the deployed tag of each container', async () => {
    serveSetting(SECURITY_SERVICE_EDGE)
    const requested: unknown[] = []
    server.use(
      http.post('/api/security/services/:serviceId/scans', async ({ request }) => {
        requested.push(await request.json())
        return HttpResponse.json(SCAN_STATUS_SCANNED)
      }),
    )
    renderApp(route)
    const scan = await screen.findByRole('button', { name: 'Scan image' })
    const deploy = within(await header()).getByRole('link', { name: 'Deploy' })
    expect(scan.compareDocumentPosition(deploy) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()

    await userEvent.click(scan)
    const dialog = await screen.findByRole('dialog', { name: 'Scan for vulnerabilities' })
    expect(within(dialog).getByText('payments-api')).toBeInTheDocument()
    expect(within(dialog).getByText(/:1\.18\.2$/)).toBeInTheDocument()
    await userEvent.click(within(dialog).getByRole('button', { name: 'Scan for vulnerabilities' }))
    await waitFor(() => expect(requested).toEqual([{ container: 'payments-api', tag: '1.18.2' }]))
  })

  it('is hidden while image scanning is off', async () => {
    serveSetting(SECURITY_SERVICE_OFF)
    renderApp(route)
    await screen.findByRole('heading', { name: 'payments-api', level: 1 })
    await new Promise((resolve) => setTimeout(resolve, 50))
    expect(screen.queryByRole('button', { name: 'Scan image' })).not.toBeInTheDocument()
  })

  it('needs Deploy', async () => {
    serveSetting(SECURITY_SERVICE_EDGE)
    server.use(http.get('/api/services/:id', () => HttpResponse.json({ ...SERVICE_PAYMENTS, can: { ...SERVICE_PAYMENTS.can, deploy: false } })))
    renderApp(route)
    const scan = await screen.findByRole('button', { name: 'Scan image' })
    expect(scan).toBeDisabled()
    expect(scan).toHaveAttribute('title', `You need Deploy on ${SERVICE_PAYMENTS.tag.name}`)
  })
})
