import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import type { RevisionsResponse } from '../../../api/types'
import { REVISIONS_PAYMENTS } from '../../../test/fixtures'
import { RevisionsTable } from './RevisionsTable'

const revisions: RevisionsResponse = {
  ...REVISIONS_PAYMENTS,
  items: [
    { ...REVISIONS_PAYMENTS.items[0], revision: 44, status: 'not_deployed' },
    { ...REVISIONS_PAYMENTS.items[0], revision: 43, status: 'rolling_out' },
    { ...REVISIONS_PAYMENTS.items[1], revision: 42, status: 'deployed' },
    { ...REVISIONS_PAYMENTS.items[1], revision: 41, status: 'previous' },
  ],
}
const base = { serviceName: 'payments-api', revisions, canDeploy: true, rolloutRunning: false, onDeploy: vi.fn() }
const row = (revision: number) => screen.getByRole('row', { name: new RegExp(`^:${revision} `) })

describe('RevisionsTable', () => {
  it('shows a status pill and the right action per row', () => {
    render(<RevisionsTable {...base} />)
    expect(screen.getByText('4 shown · newest first')).toBeInTheDocument()
    expect(within(row(44)).getByText('Not deployed')).toBeInTheDocument()
    expect(within(row(44)).getByRole('button', { name: 'Deploy' })).toBeEnabled()
    expect(within(row(43)).getByText('Rolling out')).toBeInTheDocument()
    expect(within(row(43)).queryByRole('button')).not.toBeInTheDocument()
    expect(within(row(42)).getByText('Deployed')).toBeInTheDocument()
    expect(within(row(42)).queryByRole('button')).not.toBeInTheDocument()
    expect(within(row(41)).getByText('Previous')).toBeInTheDocument()
    expect(within(row(41)).getByRole('button', { name: 'Roll back to this' })).toBeEnabled()
  })

  it('reports the revision and mode on click', async () => {
    const onDeploy = vi.fn()
    render(<RevisionsTable {...base} onDeploy={onDeploy} />)
    await userEvent.click(within(row(44)).getByRole('button', { name: 'Deploy' }))
    await userEvent.click(within(row(41)).getByRole('button', { name: 'Roll back to this' }))
    expect(onDeploy).toHaveBeenNthCalledWith(1, 44, 'deploy')
    expect(onDeploy).toHaveBeenNthCalledWith(2, 41, 'rollback')
  })

  it('disables actions with the lock title without Deploy', () => {
    render(<RevisionsTable {...base} canDeploy={false} deployDisabledReason="You need Deploy on Prod" />)
    const button = within(row(44)).getByRole('button', { name: 'Deploy' })
    expect(button).toBeDisabled()
    expect(button).toHaveAttribute('title', 'You need Deploy on Prod')
  })

  it('disables actions while a rollout runs', () => {
    render(<RevisionsTable {...base} rolloutRunning />)
    expect(within(row(44)).getByRole('button', { name: 'Deploy' })).toHaveAttribute('title', 'A rollout is already running')
    expect(within(row(44)).getByRole('button', { name: 'Deploy' })).toBeDisabled()
    expect(within(row(41)).getByRole('button', { name: 'Roll back to this' })).toBeDisabled()
  })
})
