import { fireEvent, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { describe, expect, it } from 'vitest'
import { SERVICE_PAYMENTS } from '../../../test/fixtures'
import { server } from '../../../test/msw'
import { renderApp } from '../../../test/renderApp'
import { DIGEST_GATE_CRITICAL, SCAN_GATE_CRITICAL, SECURITY_SERVICE_EDGE } from '../../../test/securityFixtures'

const route = `/services/${SERVICE_PAYMENTS.id}/task-definition`

describe('TaskDefinitionTab: image scanning', () => {
  it('register pauses on the gate and registers anyway', async () => {
    server.use(http.get('/api/security/services/:serviceId', () => HttpResponse.json({ ...SECURITY_SERVICE_EDGE, pre_deploy_scan: true })))
    const bodies: { base_revision: number; scan_gate?: unknown }[] = []
    server.use(
      http.post('/api/services/:id/revisions', async ({ request }) => {
        bodies.push((await request.json()) as { base_revision: number; scan_gate?: unknown })
        if (bodies.length === 1) {
          return HttpResponse.json({ error: { code: 'scan_gate', message: SCAN_GATE_CRITICAL.message }, scan_gate: SCAN_GATE_CRITICAL }, { status: 409 })
        }
        return HttpResponse.json(
          { revision: 43, arn: 'arn:aws:ecs:us-east-1:1:task-definition/payments-api:43', image_tag: '1.18.2', cpu: '512', memory: '1024', can_deploy: true, tag: SERVICE_PAYMENTS.tag, strategy: 'Rolling update' },
          { status: 201 },
        )
      }),
    )
    renderApp(route)
    const editor = (await screen.findByRole('textbox', { name: 'Task definition JSON' })) as HTMLTextAreaElement
    fireEvent.change(editor, { target: { value: editor.value.replace('"info"', '"debug"') } })
    await userEvent.click(screen.getByRole('button', { name: /^Review and register :\d+$/ }))
    await userEvent.click(await screen.findByRole('button', { name: /Register revision/ }))

    const panel = within(await screen.findByRole('region', { name: 'Image scan' }))
    expect(panel.getByText(/1 critical/)).toBeInTheDocument()
    await userEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Deploy anyway' }))

    await waitFor(() => expect(bodies).toHaveLength(2))
    expect(bodies[0].scan_gate).toBeUndefined()
    expect(bodies[1].scan_gate).toEqual({ deploy_anyway: true, images: [{ container: 'worker', ref: DIGEST_GATE_CRITICAL }] })
    expect(await screen.findByText('payments-api:43 is registered. Deploy it from the Deploy tab when you are ready.')).toBeInTheDocument()
  })
})
