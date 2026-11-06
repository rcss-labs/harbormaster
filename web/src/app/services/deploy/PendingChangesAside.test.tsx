import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { DEPLOY_SETTINGS_PAYMENTS, TASK_DEFINITION_EDITABLE } from '../../../test/fixtures'
import type { Definition } from '../task-definition/mainContainer'
import { PendingChangesAside } from './PendingChangesAside'
import { pendingRows, type ChangeSections, type PendingDraft } from './changeSummary'
import { setContainerImageTag, setEnvironment } from './draft'

const live = TASK_DEFINITION_EDITABLE.definition as Definition
const noSections: ChangeSections = { target: null, service: null, scaling: null }

function renderRows(draft: Partial<PendingDraft>) {
  const rows = pendingRows({ sections: noSections, liveDefinition: live, ...draft }, DEPLOY_SETTINGS_PAYMENTS)
  render(<PendingChangesAside count={rows.length} rows={rows} problems={[]} discardDisabled={false} reviewDisabled={false} onDiscard={() => {}} onReview={() => {}} />)
  return within(screen.getByRole('region', { name: 'Pending changes' }))
}

describe('PendingChangesAside', () => {
  it('shows an environment edit as label: before → after', () => {
    const edited = setEnvironment(live, 'payments-api', [
      { name: 'LOG_LEVEL', value: 'debug' },
      { name: 'FEATURE_X', value: 'on' },
    ])
    const aside = renderRows({ definitionAfter: edited })
    expect(aside.getByText('2 changes')).toBeInTheDocument()
    expect(aside.getByRole('listitem', { name: 'Environment LOG_LEVEL' })).toHaveTextContent('Environment LOG_LEVEL: info → debug')
    expect(aside.getByRole('listitem', { name: 'Environment FEATURE_X' })).toHaveTextContent('Environment FEATURE_X: Not set → on')
  })

  it('shows an image tag edit', () => {
    const aside = renderRows({ definitionAfter: setContainerImageTag(live, 'payments-api', '1.19.0') })
    expect(aside.getByRole('listitem', { name: 'Image tag' })).toHaveTextContent('Image tag: 1.18.2 → 1.19.0')
  })

  it('shows desired count and circuit breaker edits', () => {
    const aside = renderRows({
      sections: {
        ...noSections,
        service: { desired_count: 5, deployment_configuration: { circuit_breaker: { enable: false, rollback: false }, minimum_healthy_percent: 100, maximum_percent: 200 } },
      },
    })
    expect(aside.getByRole('listitem', { name: 'Desired count' })).toHaveTextContent('Desired count: 3 → 5')
    expect(aside.getByRole('listitem', { name: 'Circuit breaker' })).toHaveTextContent('Circuit breaker: on → off')
  })

  it('shows a policy edit and a removed scheduled action', () => {
    const aside = renderRows({
      sections: {
        ...noSections,
        scaling: {
          put_policies: [{ name: 'cpu-target-60', metric: 'cpu', target_value: 70, scale_in_cooldown: 300, scale_out_cooldown: 60, disable_scale_in: false, target_group_arn: '' }],
          delete_scheduled_actions: ['business-hours'],
        },
      },
    })
    expect(aside.getByRole('listitem', { name: 'Policy cpu-target-60' })).toHaveTextContent('Policy cpu-target-60: CPU % at 60, in 300 s, out 60 s → CPU % at 70, in 300 s, out 60 s')
    expect(aside.getByRole('listitem', { name: 'Scheduled action business-hours' })).toHaveTextContent('→ Removed')
  })

  it('lists problems and the loaded revision with Dismiss, and routes Discard and Review', async () => {
    const user = userEvent.setup()
    const onDiscard = vi.fn()
    const onReview = vi.fn()
    const onDismiss = vi.fn()
    render(
      <PendingChangesAside
        count={0}
        rows={[]}
        problems={['Desired count must be a whole number between 0 and 50.']}
        loadedRevision={{ text: ':41 is loaded from the revisions list', dismissLabel: 'Dismiss :41', onDismiss }}
        discardDisabled={false}
        reviewDisabled={false}
        onDiscard={onDiscard}
        onReview={onReview}
      />,
    )
    const aside = within(screen.getByRole('region', { name: 'Pending changes' }))
    expect(aside.getByText('0 changes')).toBeInTheDocument()
    expect(aside.getByRole('list', { name: 'Problems' })).toHaveTextContent('Desired count must be a whole number between 0 and 50.')
    expect(aside.getByText(':41 is loaded from the revisions list')).toBeInTheDocument()
    await user.click(aside.getByRole('button', { name: 'Dismiss :41' }))
    await user.click(aside.getByRole('button', { name: 'Discard' }))
    await user.click(aside.getByRole('button', { name: 'Review…' }))
    expect([onDismiss, onDiscard, onReview].map((fn) => fn.mock.calls.length)).toEqual([1, 1, 1])
  })
})
