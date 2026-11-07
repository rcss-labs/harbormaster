import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { http, HttpResponse } from 'msw'
import { describe, expect, it } from 'vitest'
import { ToastProvider } from '../../../components/Toast'
import { server } from '../../../test/msw'
import { PermCell } from './PermCell'

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

describe('PermCell', () => {
  it('names the cell by user, tag and preset and shows the letters', () => {
    renderCell(7)
    const opener = screen.getByRole('button', { name: 'omer on Prod: Operator' })
    expect(opener).toHaveTextContent('Operator')
    expect(opener).toHaveTextContent('VLO')
  })

  it('preset Operator sets V L O', async () => {
    const bodies: unknown[] = []
    renderCell(0, bodies)
    fireEvent.click(screen.getByRole('button', { name: 'omer on Prod: None' }))
    fireEvent.change(screen.getByLabelText('Preset for omer on Prod'), { target: { value: 'Operator' } })
    await waitFor(() => expect(bodies).toEqual([{ actions: 7 }]))
  })

  it('turning on Logs also turns on View', async () => {
    const bodies: unknown[] = []
    renderCell(0, bodies)
    fireEvent.click(screen.getByRole('button', { name: 'omer on Prod: None' }))
    fireEvent.click(screen.getByLabelText('Logs'))
    await waitFor(() => expect(bodies).toEqual([{ actions: 3 }]))
  })

  it('turning off View clears the cell', async () => {
    const bodies: unknown[] = []
    renderCell(7, bodies)
    fireEvent.click(screen.getByRole('button', { name: 'omer on Prod: Operator' }))
    fireEvent.click(screen.getByLabelText('View'))
    await waitFor(() => expect(bodies).toEqual([{ actions: 0 }]))
  })

  it('popover closes on Esc', () => {
    renderCell(7)
    fireEvent.click(screen.getByRole('button', { name: 'omer on Prod: Operator' }))
    const popover = screen.getByRole('dialog', { name: 'Permissions for omer on Prod' })
    fireEvent.keyDown(popover, { key: 'Escape' })
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })
})
