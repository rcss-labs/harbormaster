import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { RevisionsResponse } from '../../../api/types'
import { REVISIONS_PAYMENTS } from '../../../test/fixtures'
import { installPhoneMedia } from '../../../test/phone'
import { JsonEditor } from './JsonEditor'
import { RevisionsTable } from './RevisionsTable'

let restoreMedia: () => void
beforeEach(() => {
  restoreMedia = installPhoneMedia(true)
})
afterEach(() => {
  restoreMedia()
  delete document.documentElement.dataset.hmBar
})

const editor = (over: Partial<Parameters<typeof JsonEditor>[0]> = {}) => (
  <JsonEditor text={'{\n  "a": 1\n}'} initialText={'{\n  "a": 2\n}'} registerRevision={43} error={null} dirty readOnly={false} onChange={() => {}} onDiscard={() => {}} onReview={() => {}} {...over} />
)

describe('JsonEditor on phones', () => {
  it('shows the line count and a sticky bar with Discard and Review that sets data-hm-bar', () => {
    const { unmount } = render(editor())
    expect(screen.getByText('3 lines')).toBeInTheDocument()
    const bar = screen.getByRole('region', { name: 'Pending changes' })
    expect(within(bar).getByRole('button', { name: 'Discard' })).toBeEnabled()
    expect(within(bar).getByRole('button', { name: 'Review and register :43' })).toBeEnabled()
    expect(document.documentElement.dataset.hmBar).toBe('1')
    unmount()
    expect(document.documentElement.dataset.hmBar).toBeUndefined()
  })

  it('shows no bar while the draft is clean', () => {
    render(editor({ dirty: false }))
    expect(screen.queryByRole('region', { name: 'Pending changes' })).not.toBeInTheDocument()
    expect(document.documentElement.dataset.hmBar).toBeUndefined()
  })
})

const revisions: RevisionsResponse = {
  ...REVISIONS_PAYMENTS,
  items: [
    { ...REVISIONS_PAYMENTS.items[0], revision: 44, status: 'not_deployed' },
    { ...REVISIONS_PAYMENTS.items[1], revision: 42, status: 'deployed' },
    { ...REVISIONS_PAYMENTS.items[1], revision: 41, status: 'previous' },
  ],
}

describe('RevisionsTable on phones', () => {
  it('renders rows whose ... sheet offers Deploy and Roll back, with the lock reason inside the row', async () => {
    const user = userEvent.setup()
    const onDeploy = vi.fn()
    const { rerender } = render(<RevisionsTable serviceName="payments-api" revisions={revisions} canDeploy rolloutRunning={false} onDeploy={onDeploy} />)
    expect(screen.getAllByRole('row')).toHaveLength(3)
    await user.click(screen.getByRole('button', { name: 'Actions for revision :41' }))
    await user.click(screen.getByRole('menuitem', { name: 'Roll back to this' }))
    expect(onDeploy).toHaveBeenCalledWith(41, 'rollback')

    rerender(<RevisionsTable serviceName="payments-api" revisions={revisions} canDeploy={false} deployDisabledReason="You need Deploy on Prod" rolloutRunning={false} onDeploy={onDeploy} />)
    await user.click(screen.getByRole('button', { name: 'Actions for revision :44' }))
    const deploy = screen.getByRole('menuitem', { name: /^Deploy/ })
    expect(deploy).toBeDisabled()
    expect(deploy).toHaveTextContent('You need Deploy on Prod')
  })

  it('the deployed revision says it is running now', async () => {
    const user = userEvent.setup()
    render(<RevisionsTable serviceName="payments-api" revisions={revisions} canDeploy rolloutRunning={false} onDeploy={vi.fn()} />)
    await user.click(screen.getByRole('button', { name: 'Actions for revision :42' }))
    expect(screen.getByRole('menuitem', { name: /Running now/ })).toBeDisabled()
  })
})
