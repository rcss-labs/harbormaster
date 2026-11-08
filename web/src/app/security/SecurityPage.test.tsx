import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { beforeEach, describe, expect, it } from 'vitest'
import type { SecurityImageDetail, SecurityResponse } from '../../api/security'
import { server } from '../../test/msw'
import { renderApp } from '../../test/renderApp'
import {
  DIGEST_EDGE,
  DIGEST_FAILED,
  DIGEST_QUEUED,
  SECURITY,
  SECURITY_IMAGE_EDGE,
  securityScenarioHandlers,
} from '../../test/securityFixtures'

const EDGE_IMAGE = SECURITY.images[0]
const QUEUED_IMAGE = { ...EDGE_IMAGE, digest: DIGEST_QUEUED, image: '482109375512.dkr.ecr.us-east-1.amazonaws.com/payments-api:1.18.2', status: 'queued' as const, open: { ...EDGE_IMAGE.open, critical: 0, high: 0, medium: 0 }, requests: [EDGE_IMAGE.requests[0]] }
const FAILED_IMAGE = {
  ...QUEUED_IMAGE,
  digest: DIGEST_FAILED,
  image: '482109375512.dkr.ecr.us-east-1.amazonaws.com/payments-api:1.17.0',
  status: 'failed' as const,
  failure_action: 'The scan did not finish in time, even with a larger scanner task. Rescan to try again.',
}

const serveList = (response: SecurityResponse) => server.use(http.get('/api/security', () => HttpResponse.json(response)))
const serveDetail = (detail: SecurityImageDetail) =>
  server.use(http.get('/api/security/images/:digest', ({ params }) => (params.digest === detail.digest ? HttpResponse.json(detail) : HttpResponse.json({}, { status: 404 }))))
const withManyImages = () => serveList({ ...SECURITY, images: [EDGE_IMAGE, QUEUED_IMAGE, FAILED_IMAGE] })

beforeEach(() => {
  localStorage.clear()
})

const table = () => screen.findByRole('table', { name: 'Images' })
const bodyRows = (target: HTMLElement) => within(target).getAllByRole('row').slice(1)
const rowTexts = async () => bodyRows(await table()).map((row) => row.textContent ?? '')

describe('SecurityPage', () => {
  it('lists requested images with the note and no KPIs', async () => {
    renderApp('/security')
    expect(await screen.findByRole('heading', { name: 'Image security' })).toBeInTheDocument()
    expect(await screen.findByText("Only images requested from a service's Deploy tab are scanned.")).toBeInTheDocument()
    expect(screen.getByText(/^Vulnerability data built/)).toBeInTheDocument()
    const row = bodyRows(await table())[0]
    const cells = within(row).getAllByRole('cell')
    expect(cells[0]).toHaveTextContent('platform-base:3.2.0')
    expect(cells[0]).toHaveTextContent('payments-api, notifications')
    expect(cells[0]).toHaveTextContent('Scanned')
    expect(cells[1]).toHaveTextContent(/ago.*sam/)
    expect(cells.slice(2).map((cell) => cell.textContent)).toEqual(['1', '1', '1'])
    expect(screen.queryByRole('region', { name: /summary/i })).not.toBeInTheDocument()
  })

  it('filters and searches', async () => {
    withManyImages()
    const user = userEvent.setup()
    renderApp('/security')
    await waitFor(async () => expect(await rowTexts()).toHaveLength(3))

    await user.click(screen.getByRole('radio', { name: /^Critical/ }))
    await waitFor(async () => expect(await rowTexts()).toHaveLength(1))
    expect(screen.getByTestId('search')).toHaveTextContent('filter=critical')

    await user.click(screen.getByRole('radio', { name: /^Not scanned/ }))
    await waitFor(async () => expect(await rowTexts()).toHaveLength(2))

    await user.click(screen.getByRole('radio', { name: /^All/ }))
    await user.type(screen.getByRole('searchbox', { name: 'Search images' }), 'notifications')
    await waitFor(async () => expect(await rowTexts()).toHaveLength(1))
    expect(screen.getByTestId('search')).toHaveTextContent('q=notifications')
    await user.clear(screen.getByRole('searchbox', { name: 'Search images' }))
    await user.type(screen.getByRole('searchbox', { name: 'Search images' }), 'core-services')
    await waitFor(async () => expect(await rowTexts()).toHaveLength(3))
  })

  it('shows the detail with freshness and findings', async () => {
    const user = userEvent.setup()
    renderApp('/security')
    await user.click(await screen.findByText('platform-base:3.2.0'))
    expect(screen.getByTestId('search')).toHaveTextContent(`image=${encodeURIComponent(DIGEST_EDGE)}`)
    const detail = await screen.findByRole('region', { name: 'Image detail' })
    expect(await within(detail).findByText(/^Requested by sam from payments-api \(manual\)/)).toBeInTheDocument()
    expect(within(detail).getByText(/^Requested by sam from notifications \(pre_deploy\)/)).toBeInTheDocument()
    expect(within(detail).getByText(/^Scanned .* with Syft 1\.9\.0 against data built .*/)).toBeInTheDocument()
    expect(within(detail).getByText('linux/amd64')).toBeInTheDocument()
    expect(within(detail).getByText('214 packages')).toBeInTheDocument()
    const findings = within(detail).getByRole('table', { name: 'Findings' })
    const rows = bodyRows(findings)
    expect(rows).toHaveLength(3)
    expect(rows[1]).toHaveTextContent('CVE-0000-0002')
    expect(rows[1]).toHaveTextContent('libxml2')
    expect(rows[1]).toHaveTextContent('No fix yet')
    expect(rows[0]).toHaveTextContent('3.0.15-1')
  })

  it('shows partial acceptance per service', async () => {
    renderApp(`/security?image=${DIGEST_EDGE}`)
    const findings = await screen.findByRole('table', { name: 'Findings' })
    const critical = bodyRows(findings)[0]
    expect(critical).toHaveTextContent('Accepted for 1 of 2 services')
    expect(within(critical).getByText('Accepted for 1 of 2 services')).toHaveAttribute(
      'title',
      expect.stringContaining('payments-api: sam until 30 Nov 2026: Only reachable from the private subnet; patch scheduled for next sprint.'),
    )
    // Not every requesting service accepted it, so it does not read as accepted everywhere.
    expect(critical).not.toHaveTextContent('Accepted until')
  })

  it('counts services, not containers, in the partial acceptance note', async () => {
    const extraContainer = { ...SECURITY_IMAGE_EDGE.requests[0], container: 'sidecar' }
    serveDetail({ ...SECURITY_IMAGE_EDGE, requests: [...SECURITY_IMAGE_EDGE.requests, extraContainer] })
    renderApp(`/security?image=${DIGEST_EDGE}`)
    const findings = await screen.findByRole('table', { name: 'Findings' })
    expect(bodyRows(findings)[0]).toHaveTextContent('Accepted for 1 of 2 services')
  })

  it('shows Accepted until when every requesting service accepted the finding', async () => {
    const [first, ...rest] = SECURITY_IMAGE_EDGE.findings
    serveDetail({ ...SECURITY_IMAGE_EDGE, findings: [{ ...first, accepted_everywhere: true }, ...rest] })
    renderApp(`/security?image=${DIGEST_EDGE}`)
    const findings = await screen.findByRole('table', { name: 'Findings' })
    expect(bodyRows(findings)[0]).toHaveTextContent('Accepted until 30 Nov 2026')
  })

  it('shows the failure reason and action', async () => {
    withManyImages()
    serveDetail({ ...SECURITY_IMAGE_EDGE, ...FAILED_IMAGE, enabled: true, hint: '', findings: [] })
    const rescans: string[] = []
    server.use(
      http.post('/api/security/images/:digest/rescan', ({ params }) => {
        rescans.push(String(params.digest))
        return HttpResponse.json({ enabled: true, state: 'queued', image: QUEUED_IMAGE, message: '' }, { status: 202 })
      }),
    )
    renderApp(`/security?image=${DIGEST_FAILED}`)
    const detail = await screen.findByRole('region', { name: 'Image detail' })
    expect(await within(detail).findByText(FAILED_IMAGE.failure_action)).toBeInTheDocument()
    expect(within(detail).getByText('Failed')).toBeInTheDocument()
    await userEvent.click(within(detail).getByRole('button', { name: 'Rescan' }))
    await waitFor(() => expect(rescans).toEqual([DIGEST_FAILED]))
  })

  it('explains how to enable scanning when off', async () => {
    server.use(...securityScenarioHandlers('off'))
    renderApp('/security')
    expect(await screen.findByText(/Image scanning is off\. Apply the harbormaster module/)).toBeInTheDocument()
    expect(screen.queryByRole('table', { name: 'Images' })).not.toBeInTheDocument()
  })

  it('explains when the data is stale or updating', async () => {
    server.use(...securityScenarioHandlers('stale'))
    const { unmount } = renderApp('/security')
    expect(await screen.findByText(/The vulnerability data is out of date/)).toBeInTheDocument()
    expect(screen.getByText(/the bucket did not accept the write/)).toBeInTheDocument()
    unmount()

    serveList({
      ...SECURITY,
      db: { ...SECURITY.db, refreshing: true },
      scanner: { ...SECURITY.scanner, failing_since: '2026-10-31T08:00:00Z', last_error: 'ECS refused the task' },
    })
    renderApp('/security')
    expect(await screen.findByText('Updating vulnerability data')).toBeInTheDocument()
    expect(screen.getByText(/^The scanner has not succeeded since .*: ECS refused the task$/)).toBeInTheDocument()
  })

  it('hides Accept risk without the permission', async () => {
    serveDetail({ ...SECURITY_IMAGE_EDGE, can: { accept: false, rescan: true } })
    renderApp(`/security?image=${DIGEST_EDGE}`)
    await screen.findByRole('table', { name: 'Findings' })
    expect(screen.queryByRole('button', { name: 'Accept risk' })).not.toBeInTheDocument()
  })

  it('offers Accept risk only where a service the user can accept for lacks an acceptance', async () => {
    renderApp(`/security?image=${DIGEST_EDGE}`)
    const findings = await screen.findByRole('table', { name: 'Findings' })
    const [critical, high] = bodyRows(findings)
    expect(within(critical).queryByRole('button', { name: 'Accept risk' })).not.toBeInTheDocument()
    expect(within(high).getByRole('button', { name: 'Accept risk' })).toBeInTheDocument()
  })

  it('revokes one service\'s acceptance', async () => {
    const revoked: string[] = []
    server.use(
      http.delete('/api/security/acceptances/:id', ({ params }) => {
        revoked.push(String(params.id))
        return new HttpResponse(null, { status: 204 })
      }),
    )
    renderApp(`/security?image=${DIGEST_EDGE}`)
    const findings = await screen.findByRole('table', { name: 'Findings' })
    await userEvent.click(within(bodyRows(findings)[0]).getByRole('button', { name: 'Revoke' }))
    const dialog = await screen.findByRole('dialog', { name: 'Revoke acceptance of CVE-0000-0001 for payments-api' })
    await userEvent.click(within(dialog).getByRole('button', { name: 'Revoke' }))
    await waitFor(() => expect(revoked).toEqual(['9001']))
    expect(await screen.findByText('CVE-0000-0001 is open again for payments-api.')).toBeInTheDocument()
  })

  it('says when a scan found nothing or had nothing to match', async () => {
    serveDetail({ ...SECURITY_IMAGE_EDGE, findings: [] })
    const { unmount } = renderApp(`/security?image=${DIGEST_EDGE}`)
    expect(await screen.findByText('No known vulnerabilities in the last scan.')).toBeInTheDocument()
    unmount()
    serveDetail({ ...SECURITY_IMAGE_EDGE, findings: [], packages: 0 })
    renderApp(`/security?image=${DIGEST_EDGE}`)
    expect(await screen.findByText('No OS or language packages found; nothing to match.')).toBeInTheDocument()
  })
})
