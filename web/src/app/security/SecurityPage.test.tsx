import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { beforeEach, describe, expect, it } from 'vitest'
import type { SecurityBaseImage, SecurityImageDetail, SecurityResponse } from '../../api/security'
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
const packageRows = async () => within(await screen.findByRole('list', { name: 'Findings' })).getAllByRole('listitem')
/** Opens a package and returns the table of its vulnerabilities. */
const expandPackage = async (name: string) => {
  await userEvent.click(await screen.findByRole('button', { name }))
  return screen.findByRole('table', { name: `Vulnerabilities in ${name}` })
}

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
    const rows = await packageRows()
    expect(rows).toHaveLength(3)
    expect(rows[1]).toHaveTextContent('libxml2')
    expect(rows[1]).toHaveTextContent('1 vulnerability')
    expect(rows[1]).toHaveTextContent('No fix yet')
    expect(rows[0]).toHaveTextContent('3.0.15-1')
    expect(within(detail).queryByText('CVE-0000-0002')).not.toBeInTheDocument()
  })

  it('shows one row per package and lists its vulnerabilities when opened', async () => {
    const [critical, ...rest] = SECURITY_IMAGE_EDGE.findings
    const older = { ...critical, vuln_id: 'CVE-0000-0009', severity: 'high' as const, version: '1.1.1', fixed_version: '3.0.16-1', acceptances: [] }
    const unfixed = { ...critical, vuln_id: 'CVE-0000-0010', severity: 'low' as const, fixed_version: '', acceptances: [] }
    serveDetail({ ...SECURITY_IMAGE_EDGE, findings: [critical, older, unfixed, ...rest] })
    renderApp(`/security?image=${DIGEST_EDGE}`)
    const rows = await packageRows()
    expect(rows).toHaveLength(3)
    expect(rows[0]).toHaveTextContent('openssl')
    expect(rows[0]).toHaveTextContent('3 vulnerabilities')
    expect(rows[0]).toHaveTextContent('3.0.2-0ubuntu1, 1.1.1')
    expect(rows[0]).toHaveTextContent('3.0.16-1')
    expect(rows[0]).toHaveTextContent('1 without a fix')

    const vulnerabilities = await expandPackage('openssl')
    expect(bodyRows(vulnerabilities).map((row) => row.textContent)).toEqual([
      expect.stringContaining('CVE-0000-0001'),
      expect.stringContaining('CVE-0000-0009'),
      expect.stringContaining('CVE-0000-0010'),
    ])
    await userEvent.click(screen.getByRole('button', { name: 'openssl' }))
    expect(screen.queryByRole('table', { name: 'Vulnerabilities in openssl' })).not.toBeInTheDocument()
  })

  it('labels each package as an OS package or an app dependency from its type', async () => {
    const [critical, ...rest] = SECURITY_IMAGE_EDGE.findings
    const npm = { ...critical, vuln_id: 'GHSA-xxxx', package: 'node-forge', version: '1.3.1', type: 'npm', severity: 'critical' as const, acceptances: [] }
    serveDetail({ ...SECURITY_IMAGE_EDGE, findings: [critical, npm, ...rest] })
    renderApp(`/security?image=${DIGEST_EDGE}`)
    const rows = await packageRows()
    const row = (name: string) => rows.find((r) => within(r).queryByRole('button', { name }))!
    expect(within(row('openssl')).getByText('OS package')).toHaveAttribute('title', expect.stringContaining('usually come from the base image'))
    expect(within(row('node-forge')).getByText('App dependency')).toBeInTheDocument()
  })

  describe('package kind filter', () => {
    const withMixedPackages = () => {
      const [critical, ...rest] = SECURITY_IMAGE_EDGE.findings
      const npm = { ...critical, vuln_id: 'GHSA-xxxx', package: 'node-forge', version: '1.3.1', type: 'npm', severity: 'critical' as const, acceptances: [] }
      serveDetail({ ...SECURITY_IMAGE_EDGE, findings: [critical, npm, ...rest] })
    }
    const packageNames = async () => (await packageRows()).map((row) => (within(row).getAllByRole('button')[0].textContent ?? '').replace('▸', ''))

    it('shows every package by default and narrows to OS packages or app dependencies', async () => {
      withMixedPackages()
      const user = userEvent.setup()
      renderApp(`/security?image=${DIGEST_EDGE}`)
      const group = await screen.findByRole('radiogroup', { name: 'Package kind' })
      expect(within(group).getByRole('radio', { name: /^All/ })).toHaveAttribute('aria-checked', 'true')
      expect(await packageNames()).toEqual(expect.arrayContaining(['openssl', 'node-forge', 'libxml2']))

      await user.click(within(group).getByRole('radio', { name: /^App dependencies/ }))
      expect(await packageNames()).toEqual(['node-forge'])
      expect(screen.getByTestId('search')).toHaveTextContent('pkgs=app')

      await user.click(within(group).getByRole('radio', { name: /^OS packages/ }))
      expect(await packageNames()).not.toContain('node-forge')
      expect(await packageNames()).toContain('openssl')

      await user.click(within(group).getByRole('radio', { name: /^All/ }))
      expect(await packageNames()).toContain('node-forge')
      expect(screen.getByTestId('search')).not.toHaveTextContent('pkgs=')
    })

    it('restores the filter from the URL and shows a count per kind', async () => {
      withMixedPackages()
      renderApp(`/security?image=${DIGEST_EDGE}&pkgs=app`)
      const group = await screen.findByRole('radiogroup', { name: 'Package kind' })
      expect(within(group).getByRole('radio', { name: /^App dependencies/ })).toHaveAttribute('aria-checked', 'true')
      expect(within(group).getByRole('radio', { name: /^App dependencies/ })).toHaveTextContent('1')
      expect(within(group).getByRole('radio', { name: /^OS packages/ })).toHaveTextContent('3')
      expect(await packageNames()).toEqual(['node-forge'])
    })

    it('explains an empty result and keeps the control to switch back', async () => {
      renderApp(`/security?image=${DIGEST_EDGE}&pkgs=app`)
      expect(await screen.findByText('No app dependencies have known vulnerabilities.')).toBeInTheDocument()
      const group = screen.getByRole('radiogroup', { name: 'Package kind' })
      await userEvent.click(within(group).getByRole('radio', { name: /^All/ }))
      expect(await packageRows()).not.toHaveLength(0)
    })

    it('moves between the options with the arrow keys', async () => {
      withMixedPackages()
      const user = userEvent.setup()
      renderApp(`/security?image=${DIGEST_EDGE}`)
      const group = await screen.findByRole('radiogroup', { name: 'Package kind' })
      within(group).getByRole('radio', { name: /^All/ }).focus()
      await user.keyboard('{ArrowRight}')
      expect(within(group).getByRole('radio', { name: /^OS packages/ })).toHaveAttribute('aria-checked', 'true')
    })
  })

  it('accepts the risk of a whole package', async () => {
    const bodies: unknown[] = []
    server.use(
      http.post('/api/security/images/:digest/package-acceptances', async ({ request }) => {
        bodies.push(await request.json())
        return HttpResponse.json({ ids: [9200] }, { status: 201 })
      }),
    )
    renderApp(`/security?image=${DIGEST_EDGE}`)
    const [, libxml2] = await packageRows()
    await userEvent.click(within(libxml2).getByRole('button', { name: 'Accept package risk' }))
    const dialog = await screen.findByRole('dialog', { name: 'Accept risk for every vulnerability in libxml2' })
    await userEvent.type(within(dialog).getByRole('textbox', { name: 'Reason' }), 'Not reachable')
    await userEvent.click(within(dialog).getByRole('button', { name: 'Accept for 30 days' }))
    await waitFor(() => expect(bodies).toEqual([{ package_name: 'libxml2', reason: 'Not reachable', days: 30, service_ids: [EDGE_IMAGE.requests[0].service_id] }]))
    expect(await screen.findByText(/^libxml2 accepted until .* for 1 service\. Each vulnerability returns to the list when that ends\.$/)).toBeInTheDocument()
  })

  it('offers the package accept only where a service the user can accept for still counts a vulnerability', async () => {
    renderApp(`/security?image=${DIGEST_EDGE}`)
    const [openssl, libxml2] = await packageRows()
    expect(within(openssl).queryByRole('button', { name: 'Accept package risk' })).not.toBeInTheDocument()
    expect(within(libxml2).getByRole('button', { name: 'Accept package risk' })).toBeInTheDocument()
  })

  it('shows partial acceptance per service', async () => {
    renderApp(`/security?image=${DIGEST_EDGE}`)
    const critical = bodyRows(await expandPackage('openssl'))[0]
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
    expect(bodyRows(await expandPackage('openssl'))[0]).toHaveTextContent('Accepted for 1 of 2 services')
  })

  it('shows Accepted until when every requesting service accepted the finding', async () => {
    const [first, ...rest] = SECURITY_IMAGE_EDGE.findings
    serveDetail({ ...SECURITY_IMAGE_EDGE, findings: [{ ...first, accepted_everywhere: true }, ...rest] })
    renderApp(`/security?image=${DIGEST_EDGE}`)
    expect((await packageRows())[0]).toHaveTextContent('Accepted until 30 Nov 2026')
    expect(bodyRows(await expandPackage('openssl'))[0]).toHaveTextContent('Accepted until 30 Nov 2026')
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
    serveDetail({ ...SECURITY_IMAGE_EDGE, can: { accept: false, rescan: true, set_base: true } })
    renderApp(`/security?image=${DIGEST_EDGE}`)
    await expandPackage('libxml2')
    expect(screen.queryByRole('button', { name: 'Accept risk' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Accept package risk' })).not.toBeInTheDocument()
  })

  it('offers Accept risk only where a service the user can accept for lacks an acceptance', async () => {
    renderApp(`/security?image=${DIGEST_EDGE}`)
    const [critical] = bodyRows(await expandPackage('openssl'))
    const [high] = bodyRows(await expandPackage('libxml2'))
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
    await userEvent.click(within(bodyRows(await expandPackage('openssl'))[0]).getByRole('button', { name: 'Revoke' }))
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

  describe('base image', () => {
    const matchedBase: SecurityBaseImage = {
      ref: 'node:20-bookworm-slim',
      scanned_ref: 'node@sha256:abc',
      state: 'matched',
      matched_layers: 4,
      total_layers: 9,
      message: '',
    }
    const withBase = (base_image: SecurityBaseImage, extra: Partial<SecurityImageDetail> = {}): SecurityImageDetail => ({
      ...SECURITY_IMAGE_EDGE,
      base_image,
      ...extra,
    })
    const detailRegion = () => screen.findByRole('region', { name: 'Image detail' })

    it('replaces the guessed label with the measured origin of each package', async () => {
      const [openssl, ...rest] = SECURITY_IMAGE_EDGE.findings
      const npm = { ...openssl, vuln_id: 'GHSA-xxxx', package: 'node-forge', version: '1.3.1', type: 'npm', acceptances: [] }
      serveDetail(withBase(matchedBase, { findings: [{ ...openssl, origin: 'base' }, { ...npm, origin: 'app' }, ...rest] }))
      renderApp(`/security?image=${DIGEST_EDGE}`)
      const rows = await packageRows()
      const row = (name: string) => rows.find((r) => within(r).queryByRole('button', { name }))!
      expect(within(row('openssl')).getByText('Base image')).toHaveAttribute('title', 'Measured from the image layers against node:20-bookworm-slim.')
      expect(within(row('openssl')).queryByText('OS package')).not.toBeInTheDocument()
      expect(within(row('node-forge')).getByText('App layers')).toBeInTheDocument()
      // No origin on this one: the guess stays.
      expect(within(row('libxml2')).getByText('OS package')).toBeInTheDocument()
    })

    it('says Base image and app layers for a package found in both', async () => {
      const [openssl, ...rest] = SECURITY_IMAGE_EDGE.findings
      const fromApp = { ...openssl, vuln_id: 'CVE-0000-0020', origin: 'app' as const, acceptances: [] }
      serveDetail(withBase(matchedBase, { findings: [{ ...openssl, origin: 'base' }, fromApp, ...rest] }))
      renderApp(`/security?image=${DIGEST_EDGE}`)
      expect((await packageRows())[0]).toHaveTextContent('Base image and app layers')
    })

    it('shows no base image and offers to set one', async () => {
      renderApp(`/security?image=${DIGEST_EDGE}`)
      const detail = await detailRegion()
      expect(await within(detail).findByText('Base image: not set')).toBeInTheDocument()
      expect(within(detail).getByRole('button', { name: 'Set base image' })).toBeInTheDocument()
    })

    it('does not offer to set the base image without the permission', async () => {
      serveDetail(withBase(SECURITY_IMAGE_EDGE.base_image, { can: { accept: true, rescan: true, set_base: false } }))
      renderApp(`/security?image=${DIGEST_EDGE}`)
      const detail = await detailRegion()
      expect(await within(detail).findByText('Base image: not set')).toBeInTheDocument()
      expect(within(detail).queryByRole('button', { name: 'Set base image' })).not.toBeInTheDocument()
    })

    it('does not offer to change a set base image without the permission', async () => {
      serveDetail(withBase(matchedBase, { can: { accept: true, rescan: true, set_base: false } }))
      renderApp(`/security?image=${DIGEST_EDGE}`)
      const detail = await detailRegion()
      expect(await within(detail).findByText('Base image: node:20-bookworm-slim')).toBeInTheDocument()
      expect(within(detail).queryByRole('button', { name: 'Change' })).not.toBeInTheDocument()
    })

    it('saves the base image and tells the user a rescan is queued', async () => {
      const bodies: unknown[] = []
      server.use(
        http.put('/api/security/images/:digest/base-image', async ({ request }) => {
          bodies.push(await request.json())
          return HttpResponse.json(withBase({ ...matchedBase, ref: 'node:20', state: 'pending' }))
        }),
      )
      renderApp(`/security?image=${DIGEST_EDGE}`)
      const detail = await detailRegion()
      await userEvent.click(await within(detail).findByRole('button', { name: 'Set base image' }))
      expect(within(detail).getByText('The FROM image of the Dockerfile. Applies to every tag of platform-base.')).toBeInTheDocument()
      await userEvent.type(within(detail).getByRole('textbox', { name: 'Base image' }), 'node:20')
      await userEvent.click(within(detail).getByRole('button', { name: 'Save' }))
      await waitFor(() => expect(bodies).toEqual([{ base_ref: 'node:20' }]))
      expect(await screen.findByText('Base image saved. The image is queued for a rescan.')).toBeInTheDocument()
    })

    it('rejects an empty or spaced reference before calling the server', async () => {
      const bodies: unknown[] = []
      server.use(
        http.put('/api/security/images/:digest/base-image', async ({ request }) => {
          bodies.push(await request.json())
          return HttpResponse.json(withBase(matchedBase))
        }),
      )
      renderApp(`/security?image=${DIGEST_EDGE}`)
      const detail = await detailRegion()
      await userEvent.click(await within(detail).findByRole('button', { name: 'Set base image' }))
      await userEvent.click(within(detail).getByRole('button', { name: 'Save' }))
      expect(await within(detail).findByText('Enter an image reference.')).toBeInTheDocument()
      await userEvent.type(within(detail).getByRole('textbox', { name: 'Base image' }), 'node 20')
      await userEvent.click(within(detail).getByRole('button', { name: 'Save' }))
      expect(await within(detail).findByText('An image reference has no spaces.')).toBeInTheDocument()
      expect(bodies).toEqual([])
    })

    it('shows the server message when the base image is refused', async () => {
      server.use(
        http.put('/api/security/images/:digest/base-image', () =>
          HttpResponse.json({ error: { code: 'invalid', message: 'node:99 was not found in the registry.' } }, { status: 422 }),
        ),
      )
      renderApp(`/security?image=${DIGEST_EDGE}`)
      const detail = await detailRegion()
      await userEvent.click(await within(detail).findByRole('button', { name: 'Set base image' }))
      await userEvent.type(within(detail).getByRole('textbox', { name: 'Base image' }), 'node:99')
      await userEvent.click(within(detail).getByRole('button', { name: 'Save' }))
      expect(await screen.findByText('node:99 was not found in the registry.')).toBeInTheDocument()
    })

    it('clears the base image with an empty reference', async () => {
      const bodies: unknown[] = []
      serveDetail(withBase(matchedBase))
      server.use(
        http.put('/api/security/images/:digest/base-image', async ({ request }) => {
          bodies.push(await request.json())
          return HttpResponse.json(SECURITY_IMAGE_EDGE)
        }),
      )
      renderApp(`/security?image=${DIGEST_EDGE}`)
      const detail = await detailRegion()
      await userEvent.click(await within(detail).findByRole('button', { name: 'Change' }))
      expect(within(detail).getByRole('textbox', { name: 'Base image' })).toHaveValue('node:20-bookworm-slim')
      await userEvent.click(within(detail).getByRole('button', { name: 'Clear' }))
      await waitFor(() => expect(bodies).toEqual([{ base_ref: '' }]))
    })

    it('shows how many layers come from a matched base image', async () => {
      serveDetail(withBase(matchedBase))
      renderApp(`/security?image=${DIGEST_EDGE}`)
      const detail = await detailRegion()
      expect(await within(detail).findByText('Base image: node:20-bookworm-slim')).toBeInTheDocument()
      expect(within(detail).getByText('4 of 9 layers come from it')).toBeInTheDocument()
    })

    it('shows that a rescan is queued while the base image is pending', async () => {
      serveDetail(withBase({ ...matchedBase, state: 'pending', matched_layers: 0, total_layers: 0 }))
      renderApp(`/security?image=${DIGEST_EDGE}`)
      expect(await within(await detailRegion()).findByText('Rescan queued to compare layers')).toBeInTheDocument()
    })

    it.each(['mismatch', 'error'] as const)('shows the server message for a %s base image', async (state) => {
      serveDetail(withBase({ ...matchedBase, state, message: `Server says ${state}.` }))
      renderApp(`/security?image=${DIGEST_EDGE}`)
      expect(await within(await detailRegion()).findByText(`Server says ${state}.`)).toBeInTheDocument()
    })
  })
})
