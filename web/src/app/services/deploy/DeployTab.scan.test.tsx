import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { describe, expect, it } from 'vitest'
import type { ChangeSetRequest, ChangeSetResponse } from '../../../api/deploy'
import type { ScanGate, ScanStatus } from '../../../api/security'
import { CHANGESET_APPLIED, REVISION_DEFINITION_PAYMENTS_41, SERVICE_PAYMENTS } from '../../../test/fixtures'
import { server } from '../../../test/msw'
import { renderApp } from '../../../test/renderApp'
import {
  DIGEST_GATE_CRITICAL,
  DIGEST_QUEUED,
  SCAN_GATE_CRITICAL,
  SCAN_GATE_FAILED,
  SCAN_GATE_QUEUED,
  SCAN_STATUS_NONE,
  SCAN_STATUS_QUEUED,
  SCAN_STATUS_SCANNED,
  SECURITY_SERVICE_EDGE,
  SECURITY_SERVICE_OFF,
} from '../../../test/securityFixtures'

const route = `/services/${SERVICE_PAYMENTS.id}/deploy`
const SETTING_LABEL = 'Scan Image for Vulnerabilities Before Deploy'

const settingOn = { ...SECURITY_SERVICE_EDGE, pre_deploy_scan: true }
const settingOff = { ...SECURITY_SERVICE_EDGE, pre_deploy_scan: false }
const scannedWithCritical: ScanStatus = {
  ...SCAN_STATUS_SCANNED,
  image: SCAN_STATUS_SCANNED.image && { ...SCAN_STATUS_SCANNED.image, open: { ...SCAN_STATUS_SCANNED.image.open, critical: 2 } },
}

const serveSetting = (setting: typeof settingOn) => server.use(http.get('/api/security/services/:serviceId', () => HttpResponse.json(setting)))

/** Answers the change set with `reply(callNumber)` and records every body. */
function captureChangeSets(reply: (call: number) => Response) {
  const bodies: ChangeSetRequest[] = []
  server.use(
    http.post('/api/services/:id/changeset', async ({ request }) => {
      bodies.push((await request.json()) as ChangeSetRequest)
      return reply(bodies.length)
    }),
  )
  return bodies
}

const gateResponse = (gate: ScanGate) => HttpResponse.json({ error: { code: 'scan_gate', message: gate.message }, scan_gate: gate }, { status: 409 })

/** Records the query of every scan status read. `reply(n)` answers the nth read by digest; reads by container and tag answer none. */
function captureScanReads(reply: (digestRead: number) => ScanStatus) {
  const queries: URLSearchParams[] = []
  let digestReads = 0
  server.use(
    http.get('/api/security/services/:serviceId/scans', ({ request }) => {
      const query = new URL(request.url).searchParams
      queries.push(query)
      if (!query.has('digest')) return HttpResponse.json(SCAN_STATUS_NONE)
      digestReads += 1
      return HttpResponse.json(reply(digestReads))
    }),
  )
  return queries
}

const footer = () => screen.getByRole('region', { name: 'Pending changes' })
const dialog = () => screen.getByRole('dialog')
const containers = () => screen.getByRole('region', { name: 'Containers' })

async function openDeployTab(path = route) {
  renderApp(path)
  await waitFor(() => within(screen.getByRole('region', { name: 'Service' })).getByRole('spinbutton', { name: 'Desired count' }))
  await screen.findByRole('button', { name: 'Add variable' })
}

async function editEnvValue(user: ReturnType<typeof userEvent.setup>, value: string) {
  const field = within(containers()).getByLabelText('Variable value 1')
  await user.clear(field)
  await user.type(field, value)
}

/** Edits the environment and presses Register and deploy, which sends the first change set. */
async function submitDefinitionChange(user: ReturnType<typeof userEvent.setup>) {
  await editEnvValue(user, 'debug')
  await user.click(within(footer()).getByRole('button', { name: 'Review…' }))
  await user.click(within(dialog()).getByRole('button', { name: 'Register and deploy :44' }))
}

describe('DeployTab: image scanning', () => {
  it('hides the toggle and the button when scanning is off', async () => {
    let asked = false
    server.use(
      http.get('/api/security/services/:serviceId', () => {
        asked = true
        return HttpResponse.json(SECURITY_SERVICE_OFF)
      }),
    )
    await openDeployTab()
    await waitFor(() => expect(asked).toBe(true))
    await new Promise((resolve) => setTimeout(resolve, 50))
    expect(screen.queryByRole('checkbox', { name: SETTING_LABEL })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Scan for vulnerabilities' })).not.toBeInTheDocument()
  })

  it('toggle is off by default and needs Deploy', async () => {
    serveSetting({ ...settingOff, can_change_setting: false })
    await openDeployTab()
    const toggle = await screen.findByRole('checkbox', { name: SETTING_LABEL })
    expect(toggle).not.toBeChecked()
    expect(toggle).toBeDisabled()
    expect(toggle.closest('span[title]')).toHaveAttribute('title', 'You need Deploy on Prod')
    expect(screen.getByText('Rollbacks to a revision that ran before, restarts and redeploys of the running revision are never scanned or delayed.')).toBeInTheDocument()
  })

  it('saves the setting at once and says so', async () => {
    const user = userEvent.setup()
    const saved: unknown[] = []
    serveSetting(settingOff)
    server.use(
      http.put('/api/security/services/:serviceId/setting', async ({ request }) => {
        saved.push(await request.json())
        return HttpResponse.json(settingOn)
      }),
    )
    await openDeployTab()
    await user.click(await screen.findByRole('checkbox', { name: SETTING_LABEL }))
    await waitFor(() => expect(saved).toEqual([{ pre_deploy_scan: true }]))
    expect(await screen.findByText('Pre-deploy image scanning is on for payments-api.')).toBeInTheDocument()
  })

  it('button requests a scan with the setting off and shows the result', async () => {
    const user = userEvent.setup()
    serveSetting(settingOff)
    const requested: unknown[] = []
    let scanned = false
    server.use(
      http.get('/api/security/services/:serviceId/scans', () => HttpResponse.json(scanned ? SCAN_STATUS_SCANNED : SCAN_STATUS_NONE)),
      http.post('/api/security/services/:serviceId/scans', async ({ request }) => {
        requested.push(await request.json())
        scanned = true
        return HttpResponse.json(SCAN_STATUS_QUEUED)
      }),
    )
    await openDeployTab()
    await user.click(await within(containers()).findByRole('button', { name: 'Scan for vulnerabilities' }))
    await waitFor(() => expect(requested).toEqual([{ container: 'payments-api', tag: '1.18.2' }]))
    expect(await within(containers()).findByText(/Scanned .*: 0 critical, 1 high, 1 medium/)).toBeInTheDocument()
    expect(within(containers()).getByRole('link', { name: 'Open Security' })).toHaveAttribute('href', expect.stringContaining('/security'))
  })

  it('warns when the scanned image has critical findings', async () => {
    serveSetting(settingOn)
    server.use(http.get('/api/security/services/:serviceId/scans', () => HttpResponse.json(scannedWithCritical)))
    await openDeployTab()
    expect(await within(containers()).findByText('This image has 2 critical findings.')).toBeInTheDocument()
  })

  it('setting off applies without a gate panel', async () => {
    const user = userEvent.setup()
    serveSetting(settingOff)
    const bodies = captureChangeSets(() => HttpResponse.json(CHANGESET_APPLIED))
    await openDeployTab()
    await submitDefinitionChange(user)
    await waitFor(() => expect(bodies).toHaveLength(1))
    expect(bodies[0].scan_gate).toBeUndefined()
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    expect(screen.queryByText('Deploy anyway')).not.toBeInTheDocument()
  })

  it('gate waits for a queued scan then continues when clean', async () => {
    const user = userEvent.setup()
    serveSetting(settingOn)
    const bodies = captureChangeSets((call) => (call === 1 ? gateResponse(SCAN_GATE_QUEUED) : HttpResponse.json(CHANGESET_APPLIED)))
    const reads = captureScanReads((call) => (call === 1 ? SCAN_STATUS_QUEUED : SCAN_STATUS_SCANNED))
    await openDeployTab()
    await submitDefinitionChange(user)

    const panel = await screen.findByRole('region', { name: 'Image scan' })
    expect(within(panel).getByText('A scan never blocks a deploy; critical findings pause it until you choose.')).toBeInTheDocument()
    expect(within(panel).getByText('Queued')).toBeInTheDocument()

    await waitFor(() => expect(bodies).toHaveLength(2), { timeout: 8000 })
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    expect(bodies).toHaveLength(2)
    expect(bodies[1].scan_gate).toBeUndefined()
    // The gate rows are polled by digest: a changed repository or a digest-pinned image has no tag to ask by.
    const gateReads = reads.filter((query) => query.get('digest') === DIGEST_QUEUED)
    expect(gateReads.length).toBeGreaterThanOrEqual(2)
    expect(gateReads.every((query) => !query.has('tag'))).toBe(true)
  })

  it('offers Deploy anyway while the scan is still running', async () => {
    const user = userEvent.setup()
    serveSetting(settingOn)
    const bodies = captureChangeSets((call) => (call === 1 ? gateResponse(SCAN_GATE_QUEUED) : HttpResponse.json(CHANGESET_APPLIED)))
    captureScanReads(() => ({ ...SCAN_STATUS_QUEUED, state: 'scanning' }))
    await openDeployTab()
    await submitDefinitionChange(user)

    const panel = within(await screen.findByRole('region', { name: 'Image scan' }))
    expect(await panel.findByText('Scanning…')).toBeInTheDocument()
    await user.click(within(dialog()).getByRole('button', { name: 'Deploy anyway' }))

    await waitFor(() => expect(bodies).toHaveLength(2))
    expect(bodies[1].scan_gate).toEqual({ deploy_anyway: true, images: [{ container: 'payments-api', ref: DIGEST_QUEUED }] })
  })

  it('pauses on critical and deploys anyway with the acknowledgement', async () => {
    const user = userEvent.setup()
    serveSetting(settingOn)
    const bodies = captureChangeSets((call) => {
      if (call === 1) return gateResponse(SCAN_GATE_QUEUED)
      if (call === 2) return gateResponse(SCAN_GATE_CRITICAL)
      return HttpResponse.json(CHANGESET_APPLIED)
    })
    captureScanReads(() => SCAN_STATUS_SCANNED)
    await openDeployTab()
    await submitDefinitionChange(user)

    const panel = within(await screen.findByRole('region', { name: 'Image scan' }))
    expect(await panel.findByText(/1 critical/)).toBeInTheDocument()
    expect(panel.getByRole('link', { name: 'Open findings' })).toHaveAttribute('href', `/security?image=${DIGEST_GATE_CRITICAL}`)
    expect(bodies).toHaveLength(2)

    await user.click(within(dialog()).getByRole('button', { name: 'Deploy anyway' }))
    await waitFor(() => expect(bodies).toHaveLength(3))
    // The acknowledgement carries the digest of the latest 409, not the one the first answer named.
    expect(bodies[2].scan_gate).toEqual({ deploy_anyway: true, images: [{ container: 'worker', ref: DIGEST_GATE_CRITICAL }] })
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
  })

  it('pauses with the reason when the scan fails and deploys anyway', async () => {
    const user = userEvent.setup()
    serveSetting(settingOn)
    const bodies = captureChangeSets((call) => (call === 1 ? gateResponse(SCAN_GATE_FAILED) : HttpResponse.json(CHANGESET_APPLIED)))
    await openDeployTab()
    await submitDefinitionChange(user)

    const panel = within(await screen.findByRole('region', { name: 'Image scan' }))
    expect(panel.getByText(SCAN_GATE_FAILED.images[0].message)).toBeInTheDocument()
    await user.click(within(dialog()).getByRole('button', { name: 'Deploy anyway' }))

    await waitFor(() => expect(bodies).toHaveLength(2))
    expect(bodies[1].scan_gate).toEqual({ deploy_anyway: true, images: [{ container: 'app', ref: SCAN_GATE_FAILED.images[0].ref }] })
  })

  it('partial retry keeps the acknowledgement', async () => {
    const user = userEvent.setup()
    serveSetting(settingOn)
    // Registering failed, so the retry sends the definition again and the server gates it again.
    const registerFailed: ChangeSetResponse = {
      ...CHANGESET_APPLIED,
      status: 'partial',
      steps: [
        { name: 'register', status: 'failed', message: 'ECS rejected the definition.' },
        { name: 'service', status: 'skipped', message: 'Skipped because registering failed.' },
        { name: 'scaling', status: 'skipped', message: 'Skipped.' },
      ],
      registered_revision: null,
      to_revision: null,
    }
    const bodies = captureChangeSets((call) => {
      if (call === 1) return gateResponse(SCAN_GATE_CRITICAL)
      if (call === 2) return HttpResponse.json(registerFailed)
      return HttpResponse.json(CHANGESET_APPLIED)
    })
    await openDeployTab()
    await submitDefinitionChange(user)
    await user.click(await within(await screen.findByRole('dialog')).findByRole('button', { name: 'Deploy anyway' }))
    await user.click(await within(dialog()).findByRole('button', { name: 'Retry remaining steps' }))

    await waitFor(() => expect(bodies).toHaveLength(3))
    expect(bodies[2].task_definition).toEqual(bodies[1].task_definition)
    expect(bodies[2].scan_gate).toEqual({ deploy_anyway: true, images: [{ container: 'worker', ref: DIGEST_GATE_CRITICAL }] })
  })

  it('rollback by revision never shows the panel', async () => {
    const user = userEvent.setup()
    serveSetting(settingOn)
    const bodies = captureChangeSets(() => HttpResponse.json(CHANGESET_APPLIED))
    server.use(http.get('/api/services/:id/revisions/:revision', () => HttpResponse.json(REVISION_DEFINITION_PAYMENTS_41)))
    await openDeployTab(`${route}?revision=41`)
    await waitFor(() => expect(within(footer()).getByText('1 change')).toBeInTheDocument())
    await user.click(within(footer()).getByRole('button', { name: 'Review…' }))
    await user.click(within(dialog()).getByRole('button', { name: 'Deploy :41' }))

    await waitFor(() => expect(bodies).toHaveLength(1))
    expect(bodies[0].task_definition).toEqual({ revision: 41 })
    expect(bodies[0].scan_gate).toBeUndefined()
    expect(screen.queryByRole('region', { name: 'Image scan' })).not.toBeInTheDocument()
  })

  it('a revision that never ran is paused by the gate and deploys anyway with the acknowledgement', async () => {
    const user = userEvent.setup()
    serveSetting(settingOn)
    const bodies = captureChangeSets((call) => (call === 1 ? gateResponse(SCAN_GATE_FAILED) : HttpResponse.json(CHANGESET_APPLIED)))
    server.use(http.get('/api/services/:id/revisions/:revision', () => HttpResponse.json(REVISION_DEFINITION_PAYMENTS_41)))
    await openDeployTab(`${route}?revision=41`)
    await waitFor(() => expect(within(footer()).getByText('1 change')).toBeInTheDocument())
    await user.click(within(footer()).getByRole('button', { name: 'Review…' }))
    await user.click(within(dialog()).getByRole('button', { name: 'Deploy :41' }))

    expect(await screen.findByRole('region', { name: 'Image scan' })).toBeInTheDocument()
    await user.click(within(dialog()).getByRole('button', { name: 'Deploy anyway' }))
    await waitFor(() => expect(bodies).toHaveLength(2))
    expect(bodies[1].task_definition).toEqual({ revision: 41 })
    expect(bodies[1].scan_gate).toEqual({ deploy_anyway: true, images: SCAN_GATE_FAILED.images.map(({ container, ref }) => ({ container, ref })) })
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
  })

  it('Cancel waits while a resubmission is in flight, and a late 409 after closing shows no dead panel', async () => {
    const user = userEvent.setup()
    serveSetting(settingOn)
    let release = () => {}
    const held = new Promise<void>((resolve) => (release = resolve))
    const bodies = captureChangeSets(() => HttpResponse.json(CHANGESET_APPLIED))
    server.use(
      http.post('/api/services/:id/changeset', async ({ request }) => {
        bodies.push((await request.json()) as ChangeSetRequest)
        if (bodies.length === 1) return gateResponse(SCAN_GATE_QUEUED)
        await held
        return gateResponse(SCAN_GATE_CRITICAL)
      }),
    )
    captureScanReads(() => SCAN_STATUS_SCANNED)
    await openDeployTab()
    await submitDefinitionChange(user)
    await waitFor(() => expect(bodies).toHaveLength(2))

    // The server may still apply the request in flight, so Cancel cannot honestly promise anything until it answers.
    expect(within(dialog()).getByRole('button', { name: 'Cancel' })).toBeDisabled()
    await user.keyboard('{Escape}')
    release()
    await new Promise((resolve) => setTimeout(resolve, 100))
    expect(screen.queryByRole('region', { name: 'Image scan' })).not.toBeInTheDocument()
  })

  it('a repeated pending 409 for the same digest still continues', async () => {
    const user = userEvent.setup()
    serveSetting(settingOn)
    const bodies = captureChangeSets((call) => (call < 3 ? gateResponse(SCAN_GATE_QUEUED) : HttpResponse.json(CHANGESET_APPLIED)))
    captureScanReads(() => SCAN_STATUS_SCANNED)
    await openDeployTab()
    await submitDefinitionChange(user)
    await waitFor(() => expect(bodies).toHaveLength(3))
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
  })

  it('cancel closes without posting again', async () => {
    const user = userEvent.setup()
    serveSetting(settingOn)
    const bodies = captureChangeSets(() => gateResponse(SCAN_GATE_CRITICAL))
    await openDeployTab()
    await submitDefinitionChange(user)
    await screen.findByRole('region', { name: 'Image scan' })

    await user.click(within(dialog()).getByRole('button', { name: 'Cancel' }))
    await waitFor(() => expect(screen.queryByRole('region', { name: 'Image scan' })).not.toBeInTheDocument())
    await new Promise((resolve) => setTimeout(resolve, 100))
    expect(bodies).toHaveLength(1)
  })
})
