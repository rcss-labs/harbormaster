import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { http, HttpResponse } from 'msw'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { ToastProvider } from '../../../components/Toast'
import { server } from '../../../test/msw'
import { installPhoneMedia } from '../../../test/phone'
import { PermCell } from './PermCell'

let restoreMedia: () => void
beforeEach(() => {
  restoreMedia = installPhoneMedia(true)
})
afterEach(() => restoreMedia())

function renderCell(actions: number, bodies: unknown[] = []) {
  server.use(
    http.put('/api/permissions/3/3', async ({ request }) => {
      const body = (await request.json()) as { actions: number }
      bodies.push(body)
      return HttpResponse.json(body)
    }),
  )
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <ToastProvider>
        <PermCell userId={3} username="omer" tagId={3} tagName="Prod" actions={actions} />
      </ToastProvider>
    </QueryClientProvider>,
  )
}

describe('PermCell on a phone', () => {
  it('opens a sheet with the preset select and the seven toggles', () => {
    renderCell(7)
    fireEvent.click(screen.getByRole('button', { name: 'omer on Prod: Operator' }))
    const sheet = screen.getByRole('dialog', { name: 'Permissions for omer on Prod' })
    expect(within(sheet).getByLabelText('Preset for omer on Prod')).toHaveValue('Operator')
    for (const name of ['View', 'Logs', 'Operate', 'Deploy', 'Edit task definition', 'Terminal', 'Accept risk']) {
      expect(within(sheet).getByLabelText(name)).toBeInTheDocument()
    }
    expect(within(sheet).getByText('Any action also turns on View. Turning View off clears the rest.')).toBeInTheDocument()
  })

  it('sheet keeps the preset/toggle rule: an action turns View on, View off clears everything', async () => {
    const bodies: unknown[] = []
    renderCell(0, bodies)
    fireEvent.click(screen.getByRole('button', { name: 'omer on Prod: None' }))
    fireEvent.click(screen.getByLabelText('Logs'))
    await waitFor(() => expect(bodies).toEqual([{ actions: 3 }]))
  })

  it('turning View off sends 0', async () => {
    const bodies: unknown[] = []
    renderCell(7, bodies)
    fireEvent.click(screen.getByRole('button', { name: 'omer on Prod: Operator' }))
    fireEvent.click(screen.getByLabelText('View'))
    await waitFor(() => expect(bodies).toEqual([{ actions: 0 }]))
  })

  it('a custom mask stays readable and selectable as Custom', () => {
    renderCell(35)
    fireEvent.click(screen.getByRole('button', { name: 'omer on Prod: Custom' }))
    expect(screen.getByLabelText('Preset for omer on Prod')).toHaveValue('Custom')
  })
})
