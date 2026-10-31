import { fireEvent, screen, waitFor, within } from '@testing-library/react'
import { http, HttpResponse } from 'msw'
import { describe, expect, it } from 'vitest'
import type { AccountsDTO, RegionResult } from '../../../api/types'
import { ACCOUNTS } from '../../../test/fixtures'
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

    renderApp('/admin/accounts')
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

  it('status pill from last_error', async () => {
    const broken: AccountsDTO = {
      ...ACCOUNTS,
      items: ACCOUNTS.items.map((account, index) =>
        index === 0 ? { ...account, status: 'error', last_error: 'AccessDenied: role trust policy rejects the external ID' } : account,
      ),
    }
    server.use(http.get('/api/accounts', () => HttpResponse.json(broken)))

    renderApp('/admin/accounts')
    const pill = await screen.findByText('Error')
    expect(pill).toHaveAttribute('title', 'AccessDenied: role trust policy rejects the external ID')
  })
})
