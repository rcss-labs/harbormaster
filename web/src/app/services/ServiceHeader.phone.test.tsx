import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import type { ServiceDTO } from '../../api/types'
import { SERVICE_PAYMENTS } from '../../test/fixtures'
import { installPhoneMedia } from '../../test/phone'
import { renderWithProviders } from '../../test/setup'
import { AuthProvider } from '../auth/AuthProvider'
import { ServiceHeader } from './ServiceHeader'

let restoreMedia: () => void
beforeEach(() => {
  localStorage.clear()
  restoreMedia = installPhoneMedia(true)
})
afterEach(() => restoreMedia())

const locked: ServiceDTO = { ...SERVICE_PAYMENTS, can: { ...SERVICE_PAYMENTS.can, operate: false, deploy: false, edit: false } }
const rolling: ServiceDTO = { ...SERVICE_PAYMENTS, deployments: [{ ...SERVICE_PAYMENTS.deployments[0], rollout_state: 'IN_PROGRESS' }] }

const render = (service: ServiceDTO, tab = 'overview') => renderWithProviders(
    <AuthProvider>
      <ServiceHeader service={service} tab={tab} />
    </AuthProvider>,
  )

describe('ServiceHeader on phones', () => {
  it('compact row, Deploy link hidden when locked, actions sheet rows and reasons', async () => {
    const user = userEvent.setup()
    render(SERVICE_PAYMENTS)
    // The name stays for screen readers; the row shows the revision link and the Deploy link.
    expect(screen.getByRole('heading', { name: 'payments-api', level: 1 })).toHaveClass('hm-phone-title-sr')
    expect(screen.getByRole('link', { name: `:${SERVICE_PAYMENTS.revision}` })).toHaveAttribute('href', `/services/${SERVICE_PAYMENTS.id}/task-definition`)
    expect(screen.getByRole('link', { name: 'Deploy' })).toHaveAttribute('href', `/services/${SERVICE_PAYMENTS.id}/deploy`)
    expect(screen.queryByRole('button', { name: 'Restart (new deployment)' })).not.toBeInTheDocument()
    expect(screen.queryByRole('group', { name: 'Desired count' })).not.toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: 'More actions for payments-api' }))
    const menu = screen.getByRole('menu', { name: 'More actions for payments-api' })
    const names = within(menu)
      .getAllByRole('menuitem')
      .map((item) => item.textContent)
    expect(names).toEqual(['Restart', `Change desired countNow ${SERVICE_PAYMENTS.desired}.`, 'Deploy', 'Pin to Overview and Services', 'AWS console'])
    expect(within(menu).getByRole('menuitem', { name: /^Restart/ })).toBeEnabled()
  })

  it('hides the Deploy link on the Deploy tab and when locked, and gives the locked reasons in the sheet', async () => {
    const user = userEvent.setup()
    const { unmount } = render(SERVICE_PAYMENTS, 'deploy')
    expect(screen.queryByRole('link', { name: 'Deploy' })).not.toBeInTheDocument()
    unmount()

    render(locked)
    expect(screen.queryByRole('link', { name: 'Deploy' })).not.toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'More actions for payments-api' }))
    const menu = screen.getByRole('menu')
    const tag = SERVICE_PAYMENTS.tag.name
    const restart = within(menu).getByRole('menuitem', { name: /^Restart/ })
    expect(restart).toBeDisabled()
    expect(restart).toHaveTextContent(`You need Operate on ${tag}`)
    const desired = within(menu).getByRole('menuitem', { name: /^Change desired count/ })
    expect(desired).toBeDisabled()
    expect(desired).toHaveTextContent(`Now ${SERVICE_PAYMENTS.desired}. Needs Operate on ${tag}.`)
    const deploy = within(menu).getByRole('menuitem', { name: /^Deploy/ })
    expect(deploy).toBeDisabled()
    expect(deploy).toHaveTextContent(`You need Edit, Deploy or Operate on ${tag}`)
  })

  it('explains a running rollout inside the Restart row', async () => {
    const user = userEvent.setup()
    render(rolling)
    await user.click(screen.getByRole('button', { name: 'More actions for payments-api' }))
    const restart = within(screen.getByRole('menu')).getByRole('menuitem', { name: /^Restart/ })
    expect(restart).toBeDisabled()
    expect(restart).toHaveTextContent('A rollout is already running')
  })

  it('Pin label text follows the pinned state', async () => {
    const user = userEvent.setup()
    render(SERVICE_PAYMENTS)
    await user.click(screen.getByRole('button', { name: 'More actions for payments-api' }))
    await user.click(screen.getByRole('menuitem', { name: 'Pin to Overview and Services' }))
    await user.click(screen.getByRole('button', { name: 'More actions for payments-api' }))
    expect(screen.getByRole('menuitem', { name: 'Unpin from Overview and Services' })).toBeInTheDocument()
  })
})
