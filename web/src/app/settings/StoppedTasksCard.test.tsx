import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { describe, expect, it } from 'vitest'
import { server } from '../../test/msw'
import { renderApp } from '../../test/renderApp'

const WINDOW_LABEL = 'Show stopped tasks from the last X minutes'

/** Records each PUT body so tests can assert that invalid input never reaches the server. */
function captureSettingsPuts() {
  const bodies: unknown[] = []
  server.use(
    http.put('/api/settings', async ({ request }) => {
      const body = await request.json()
      bodies.push(body)
      return HttpResponse.json(body)
    }),
  )
  return bodies
}

describe('StoppedTasksCard', () => {
  it('shows the current window', async () => {
    renderApp('/settings/general')
    const input = await screen.findByLabelText(WINDOW_LABEL)
    await waitFor(() => expect(input).toHaveValue(10))
  })

  it('spells the window out in hours and minutes', async () => {
    renderApp('/settings/general')
    const input = await screen.findByLabelText(WINDOW_LABEL)
    await waitFor(() => expect(input).toHaveValue(10))
    expect(screen.getByText('10 min')).toBeInTheDocument()
    const user = userEvent.setup()
    await user.clear(input)
    await user.type(input, '90')
    expect(screen.getByText('1 h 30 min')).toBeInTheDocument()
    await user.clear(input)
    await user.type(input, '120')
    expect(screen.getByText('2 h')).toBeInTheDocument()
  })

  it('rejects 0 and 1441 without a request', async () => {
    const bodies = captureSettingsPuts()
    renderApp('/settings/general')
    const input = await screen.findByLabelText(WINDOW_LABEL)
    await waitFor(() => expect(input).toHaveValue(10))
    const user = userEvent.setup()

    for (const value of ['0', '1441']) {
      await user.clear(input)
      await user.type(input, value)
      await user.click(screen.getByRole('button', { name: 'Save' }))
      expect(await screen.findByText('Enter a number from 1 to 1440.')).toBeInTheDocument()
    }
    expect(bodies).toEqual([])
  })

  it('saves a valid value and confirms with a toast', async () => {
    const bodies = captureSettingsPuts()
    renderApp('/settings/general')
    const input = await screen.findByLabelText(WINDOW_LABEL)
    await waitFor(() => expect(input).toHaveValue(10))
    const user = userEvent.setup()

    await user.clear(input)
    await user.type(input, '30')
    await user.click(screen.getByRole('button', { name: 'Save' }))

    expect(await screen.findByText('Saved.')).toBeInTheDocument()
    expect(bodies).toEqual([{ stopped_tasks_window_minutes: 30 }])
  })

  it('follows the server value after a save and refetch', async () => {
    renderApp('/settings/general')
    const input = await screen.findByLabelText(WINDOW_LABEL)
    await waitFor(() => expect(input).toHaveValue(10))
    server.use(http.get('/api/settings', () => HttpResponse.json({ stopped_tasks_window_minutes: 45 })))
    const user = userEvent.setup()

    await user.clear(input)
    await user.type(input, '30')
    await user.click(screen.getByRole('button', { name: 'Save' }))

    await waitFor(() => expect(input).toHaveValue(45))
  })
})
