import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { SERVICE_PAYMENTS } from '../../test/fixtures'
import { installPhoneMedia } from '../../test/phone'
import { renderWithProviders } from '../../test/setup'
import { OverviewTab } from './OverviewTab'

let restoreMedia: () => void
beforeEach(() => {
  restoreMedia = installPhoneMedia(true)
})
afterEach(() => restoreMedia())

describe('OverviewTab on phones', () => {
  it('Rollout panel carries Restart, Desired count and Open Deploy', async () => {
    const user = userEvent.setup()
    renderWithProviders(<OverviewTab service={SERVICE_PAYMENTS} />)
    const panel = screen.getByRole('region', { name: 'Rollout' })
    expect(within(panel).getByRole('button', { name: 'Restart' })).toBeEnabled()
    expect(within(panel).getByRole('link', { name: 'Open Deploy' })).toHaveAttribute('href', `/services/${SERVICE_PAYMENTS.id}/deploy`)
    await user.click(within(panel).getByRole('button', { name: 'Desired count' }))
    expect(screen.getByRole('dialog', { name: 'Change desired count' })).toBeInTheDocument()
  })

  it('Restart explains why it is disabled without Operate', () => {
    const service = { ...SERVICE_PAYMENTS, can: { ...SERVICE_PAYMENTS.can, operate: false } }
    renderWithProviders(<OverviewTab service={service} />)
    const restart = within(screen.getByRole('region', { name: 'Rollout' })).getByRole('button', { name: 'Restart' })
    expect(restart).toBeDisabled()
    // Hover does not exist on a phone: the reason is text in the panel, once for both locked buttons.
    expect(within(screen.getByRole('region', { name: 'Rollout' })).getAllByText(`You need Operate on ${service.tag.name}`)).toHaveLength(1)
    expect(within(screen.getByRole('region', { name: 'Rollout' })).getByRole('button', { name: 'Desired count' })).toBeDisabled()
  })

  it('running tasks get an actions sheet with Logs and Stop task', async () => {
    const user = userEvent.setup()
    renderWithProviders(<OverviewTab service={SERVICE_PAYMENTS} />)
    const task = SERVICE_PAYMENTS.running_tasks[0]
    await user.click(screen.getByRole('button', { name: `Actions for task ${task.short_id}` }))
    const menu = screen.getByRole('menu', { name: `Actions for task ${task.short_id}` })
    expect(within(menu).getByRole('menuitem', { name: 'Logs' })).toHaveAttribute('href', `/services/${SERVICE_PAYMENTS.id}/logs`)
    expect(within(menu).getByRole('menuitem', { name: 'Stop task' })).toBeEnabled()
  })
})
