import { fireEvent, screen, waitFor, within } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { TASKS } from '../../test/fixtures'
import { renderApp } from '../../test/renderApp'

describe('Tasks', () => {
  it('seg buttons show counts and set ?filter=', async () => {
    renderApp('/tasks?keep=1')
    expect(await screen.findByRole('button', { name: `Problems first ${TASKS.counts.problems}` })).toHaveAttribute('aria-pressed', 'true')
    expect(screen.getByRole('button', { name: `Running ${TASKS.counts.running}` })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: `Stopped ${TASKS.counts.stopped}` })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: `All ${TASKS.counts.all}` })).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: `Stopped ${TASKS.counts.stopped}` }))
    await waitFor(() => expect(screen.getByTestId('search')).toHaveTextContent('filter=stopped'))
    expect(screen.getByTestId('search')).toHaveTextContent('keep=1')
  })

  it('error-exit row has bad stripe and "Stopped (exit 137)"', async () => {
    renderApp('/tasks')
    const [pill] = await screen.findAllByText('Stopped (exit 137)')
    const row = pill.closest('tr')!
    expect(row).toHaveClass('stripe', 'bad')
  })

  it('standalone row shows group and started_by', async () => {
    renderApp('/tasks?filter=running')
    const row = (await screen.findByText('family:release-build')).closest('tr')!
    expect(within(row).getByText('Standalone · started by events-rule/nightly-release')).toBeInTheDocument()
  })
})
