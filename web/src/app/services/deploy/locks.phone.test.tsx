import { render, screen, within } from '@testing-library/react'
import { useState } from 'react'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import type { DeploySettingsResponse } from '../../../api/deploy'
import { withCan, withoutLoadBalancer } from '../../../test/deployVariants'
import { DEPLOY_SETTINGS_PAYMENTS } from '../../../test/fixtures'
import { imageTagsHandler } from '../../../test/imageTags'
import { server } from '../../../test/msw'
import { installPhoneMedia } from '../../../test/phone'
import { renderWithProviders } from '../../../test/setup'
import type { Definition } from '../task-definition/mainContainer'
import { ContainersPanel } from './ContainersPanel'
import { ScalingPanel } from './ScalingPanel'
import { ServicePanel } from './ServicePanel'
import { TaskSizePanel } from './TaskSizePanel'
import { scalingDraftFromSettings } from './scalingDraft'
import { serviceDraftFromSettings } from './serviceDraft'

// On a phone there is no hover, so every lock reason must be visible text inside the panel or row, not a title alone.
let restorePhone: () => void
beforeEach(() => {
  restorePhone = installPhoneMedia(true)
})
afterEach(() => restorePhone())

const definition: Definition = {
  family: 'api',
  cpu: '512',
  memory: '1024',
  containerDefinitions: [
    {
      name: 'payments-api',
      image: '482109375512.dkr.ecr.us-east-1.amazonaws.com/payments-api:1.18.2',
      essential: true,
    },
    {
      name: 'migrator',
      image: '482109375512.dkr.ecr.us-east-1.amazonaws.com/migrator:2.2.1',
      essential: false,
    },
  ],
}

const panel = (name: string) => within(screen.getByRole('region', { name }))

function ServiceHarness({ settings }: { settings: DeploySettingsResponse }) {
  const [draft, setDraft] = useState(() => serviceDraftFromSettings(settings))
  return <ServicePanel settings={settings} draft={draft} onChange={setDraft} tagName="Dev" />
}

function ScalingHarness({ settings }: { settings: DeploySettingsResponse }) {
  const [draft, setDraft] = useState(() => scalingDraftFromSettings(settings))
  return <ScalingPanel settings={settings} draft={draft} onChange={setDraft} desiredCount={settings.desired_count} tagName="Dev" onRetry={() => {}} />
}

describe('lock reasons on a phone', () => {
  it('Service panel without Deploy shows one visible note, and none for the Operate controls it can use', () => {
    render(
      <ServiceHarness
        settings={withCan(DEPLOY_SETTINGS_PAYMENTS, {
          operate: true,
          deploy: false,
        })}
      />,
    )
    expect(panel('Service').getAllByText('You need Deploy on Dev')).toHaveLength(1)
    expect(panel('Service').queryByText('You need Operate on Dev')).not.toBeInTheDocument()
  })

  it('Service panel without Operate and Deploy shows each reason once', () => {
    render(
      <ServiceHarness
        settings={withCan(DEPLOY_SETTINGS_PAYMENTS, {
          operate: false,
          deploy: false,
        })}
      />,
    )
    expect(panel('Service').getAllByText('You need Operate on Dev')).toHaveLength(1)
    expect(panel('Service').getAllByText('You need Deploy on Dev')).toHaveLength(1)
  })

  it('Service panel says why the grace period is disabled without a load balancer', () => {
    render(<ServiceHarness settings={withoutLoadBalancer(DEPLOY_SETTINGS_PAYMENTS)} />)
    expect(panel('Service').getByText('Only services behind a load balancer have a health check grace period')).toBeVisible()
  })

  it('Containers panel without Edit shows one visible note', () => {
    server.use(imageTagsHandler())
    renderWithProviders(<ContainersPanel serviceId="svc" definition={definition} deployed={definition} onChange={() => {}} canEdit={false} isAdmin={false} tagName="Dev" />)
    expect(panel('Containers').getAllByText('You need Edit on Dev')).toHaveLength(1)
  })

  it('the container switcher is Chips on a phone', () => {
    server.use(imageTagsHandler())
    renderWithProviders(<ContainersPanel serviceId="svc" definition={definition} deployed={definition} onChange={() => {}} canEdit isAdmin={false} tagName="Dev" />)
    const group = screen.getByRole('group', { name: 'Container' })
    expect(group).toHaveClass('hm-chips')
    expect(within(group).getByRole('button', { name: 'migrator' })).toHaveAttribute('aria-pressed', 'false')
  })

  it('Task size without Edit shows one visible note', () => {
    render(<TaskSizePanel definition={definition} onChange={() => {}} canEdit={false} tagName="Dev" />)
    expect(panel('Task size').getAllByText('You need Edit on Dev')).toHaveLength(1)
  })

  it('Auto scaling without Operate shows one note for the whole panel', () => {
    render(<ScalingHarness settings={withCan(DEPLOY_SETTINGS_PAYMENTS, { operate: false })} />)
    expect(panel('Auto scaling').getAllByText('You need Operate on Dev')).toHaveLength(1)
  })

  it('Auto scaling says why Add policy is disabled when the policies could not be read', () => {
    const settings = {
      ...DEPLOY_SETTINGS_PAYMENTS,
      partial: [{ part: 'scaling_policies', code: 'throttled', message: 'Throttled' }],
    }
    render(<ScalingHarness settings={settings} />)
    expect(screen.getByRole('button', { name: 'Add policy' })).toBeDisabled()
    // The reason sits directly under the button, not only in the panel's error.
    expect(screen.getByRole('button', { name: 'Add policy' }).parentElement?.nextElementSibling).toHaveTextContent('Scaling policies could not be read')
  })
})
