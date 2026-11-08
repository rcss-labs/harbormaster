import { fireEvent, screen, waitFor, within } from '@testing-library/react'
import { http, HttpResponse } from 'msw'
import { describe, expect, it } from 'vitest'
import type { AccountsDTO, RegionResult } from '../../../api/types'
import { ACCOUNTS, POLLER } from '../../../test/fixtures'
import { renderApp } from '../../../test/renderApp'
import { server } from '../../../test/msw'

const ROLE_ARN = 'arn:aws:iam::123456789012:role/harbormaster-access'

describe('Accounts', () => {
  it('Test and connect shows per-region results and posts on success', async () => {
    const testBodies: unknown[] = []
    const connectBodies: unknown[] = []
    let releaseConnect: () => void = () => {}
    const connectGate = new Promise<void>((resolve) => {
      releaseConnect = resolve
    })
    server.use(
      http.post('/api/accounts/test', async ({ request }) => {
        const body = (await request.json()) as { regions: string[] }
        testBodies.push(body)
        const regions: RegionResult[] = body.regions.map((region) => ({ region, ok: true, clusters: 3, error: '' }))
        return HttpResponse.json({ regions })
      }),
      http.post('/api/accounts', async ({ request }) => {
        connectBodies.push(await request.json())
        await connectGate
        return HttpResponse.json(ACCOUNTS.items[0], { status: 201 })
      }),
    )

    renderApp('/settings/accounts')
    expect(await screen.findByRole('heading', { level: 2, name: 'AWS accounts' })).toBeInTheDocument()
    fireEvent.click(await screen.findByRole('button', { name: 'Connect account' }))
    const dialog = screen.getByRole('dialog')
    fireEvent.change(within(dialog).getByLabelText('Display name'), { target: { value: 'staging-example-2' } })
    fireEvent.change(within(dialog).getByLabelText('Role ARN'), { target: { value: ROLE_ARN } })
    await within(dialog).findByDisplayValue(ACCOUNTS.items[0].external_id)

    fireEvent.click(within(dialog).getByRole('button', { name: 'Test and connect' }))
    expect(await within(dialog).findByText('✓ us-east-1 · 3 clusters')).toBeInTheDocument()
    expect(within(dialog).getByText('✓ eu-west-1 · 3 clusters')).toBeInTheDocument()
    expect(within(dialog).getByText('✓ eu-central-1 · 3 clusters')).toBeInTheDocument()

    releaseConnect()
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    expect(testBodies).toEqual([
      { role_arn: ROLE_ARN, external_id: ACCOUNTS.items[0].external_id, regions: ['us-east-1', 'eu-west-1', 'eu-central-1'] },
    ])
    expect(connectBodies).toEqual([
      {
        name: 'staging-example-2',
        role_arn: ROLE_ARN,
        external_id: ACCOUNTS.items[0].external_id,
        regions: ['us-east-1', 'eu-west-1', 'eu-central-1'],
      },
    ])
  })

  it('keeps one external ID while the connect dialog is open', async () => {
    const issuedIds: string[] = []
    const connectBodies: { external_id: string }[] = []
    server.use(
      http.get('/api/accounts/external-id', () => {
        const externalId = `external-id-${issuedIds.length + 1}`
        issuedIds.push(externalId)
        return HttpResponse.json({ external_id: externalId })
      }),
      http.post('/api/accounts', async ({ request }) => {
        connectBodies.push((await request.json()) as { external_id: string })
        return HttpResponse.json(ACCOUNTS.items[0], { status: 201 })
      }),
    )

    renderApp('/settings/accounts')
    fireEvent.click(await screen.findByRole('button', { name: 'Connect account' }))
    const dialog = screen.getByRole('dialog')
    await waitFor(() => expect(issuedIds).toHaveLength(1))
    const firstId = issuedIds[0]
    expect(await within(dialog).findByDisplayValue(firstId)).toBeInTheDocument()

    // Returning from the AWS console refocuses the window; the field must not pick up a new ID.
    window.dispatchEvent(new Event('focus'))
    window.dispatchEvent(new Event('visibilitychange'))
    await new Promise((resolve) => setTimeout(resolve, 50))
    expect(issuedIds).toHaveLength(1)
    expect(within(dialog).getByDisplayValue(firstId)).toBeInTheDocument()

    fireEvent.change(within(dialog).getByLabelText('Display name'), { target: { value: 'staging-example-2' } })
    fireEvent.change(within(dialog).getByLabelText('Role ARN'), { target: { value: ROLE_ARN } })
    fireEvent.click(within(dialog).getByRole('button', { name: 'Test and connect' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    expect(connectBodies.map((body) => body.external_id)).toEqual([firstId])
  })

  it('error expands', async () => {
    const broken: AccountsDTO = {
      ...ACCOUNTS,
      items: ACCOUNTS.items.map((account, index) =>
        index === 0 ? { ...account, status: 'error', last_error: 'AccessDenied: role trust policy rejects the external ID' } : account,
      ),
    }
    server.use(http.get('/api/accounts', () => HttpResponse.json(broken)))

    renderApp('/settings/accounts')
    expect(await screen.findByText('Error')).toBeInTheDocument()
    const summary = screen.getByText('Show AWS error')
    const details = summary.closest('details') as HTMLDetailsElement
    expect(details.open).toBe(false)
    fireEvent.click(summary)
    expect(details.open).toBe(true)
    expect(within(details).getByText('AccessDenied: role trust policy rejects the external ID')).toBeInTheDocument()
  })

  it('has no AWS error disclosure for a healthy account', async () => {
    renderApp('/settings/accounts')
    await screen.findByRole('table', { name: 'AWS accounts' })
    expect(screen.queryByText('Show AWS error')).not.toBeInTheDocument()
  })

  it('shows "Slowed by AWS throttling" for an account whose poller is throttled', async () => {
    server.use(
      http.get('/api/poller', () =>
        HttpResponse.json({
          items: POLLER.items.map((entry) =>
            entry.account_id === ACCOUNTS.items[0].account_id && entry.region === 'eu-west-1' ? { ...entry, throttled: true } : entry,
          ),
        }),
      ),
    )
    renderApp('/settings/accounts')
    const row = within(await screen.findByRole('table', { name: 'AWS accounts' })).getByText('homelab-admin').closest('[role="row"]') as HTMLElement
    expect(await within(row).findByText('Slowed by AWS throttling')).toBeInTheDocument()
    const other = screen.getByText('staging-example', { selector: 'div' }).closest('[role="row"]') as HTMLElement
    expect(within(other).queryByText('Slowed by AWS throttling')).not.toBeInTheDocument()
  })

  it('shows "Data is N min old" and keeps the raw error behind Show AWS error when the poller is failing', async () => {
    const lastOk = new Date(Date.now() - 12 * 60_000).toISOString()
    server.use(
      http.get('/api/poller', () =>
        HttpResponse.json({
          items: POLLER.items.map((entry) =>
            entry.account_id === ACCOUNTS.items[0].account_id && entry.region === 'us-east-1'
              ? { ...entry, failing: true, last_error: 'ExpiredToken', last_ok_at: lastOk }
              : entry,
          ),
        }),
      ),
    )
    renderApp('/settings/accounts')
    const row = within(await screen.findByRole('table', { name: 'AWS accounts' })).getByText('homelab-admin').closest('[role="row"]') as HTMLElement
    expect(await within(row).findByText('Data is 12 min old')).toBeInTheDocument()
    const details = within(row).getByText('Show AWS error').closest('details') as HTMLDetailsElement
    expect(details.open).toBe(false)
    expect(within(details).getByText('ExpiredToken')).toBeInTheDocument()
  })

  it('Test now posts the account role, external id and regions and reports each region', async () => {
    const bodies: unknown[] = []
    server.use(
      http.post('/api/accounts/test', async ({ request }) => {
        const body = (await request.json()) as { regions: string[] }
        bodies.push(body)
        return HttpResponse.json({
          regions: body.regions.map((region) => ({ region, ok: region !== 'eu-west-1', clusters: 2, error: region === 'eu-west-1' ? 'AccessDenied' : '' })),
        })
      }),
    )
    renderApp('/settings/accounts')
    const row = within(await screen.findByRole('table', { name: 'AWS accounts' })).getByText('homelab-admin').closest('[role="row"]') as HTMLElement
    fireEvent.click(within(row).getByRole('button', { name: 'Test now' }))
    expect(await screen.findByText(/eu-west-1: AccessDenied/)).toBeInTheDocument()
    // The same results stay on the row, under Show AWS error.
    const details = (await within(row).findByText('Show AWS error')).closest('details') as HTMLDetailsElement
    expect(within(details).getByText('eu-west-1 · AccessDenied')).toBeInTheDocument()
    expect(within(details).getByText('us-east-1 · 2 clusters')).toBeInTheDocument()
    expect(bodies).toEqual([
      {
        role_arn: ACCOUNTS.items[0].role_arn,
        external_id: ACCOUNTS.items[0].external_id,
        regions: ACCOUNTS.items[0].regions,
      },
    ])
  })
})
