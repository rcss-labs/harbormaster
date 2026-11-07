import { screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { SERVICE_PAYMENTS } from '../../test/fixtures'
import { renderWithProviders } from '../../test/setup'
import { OverviewTab } from './OverviewTab'

/** The value cell of an Overview row, found by its label. */
const valueOf = (label: string) => screen.getByText(label, { selector: 'dt' }).nextElementSibling

describe('OverviewTab', () => {
  it('Task role says None when the task definition has no task role', () => {
    const service = { ...SERVICE_PAYMENTS, task_definition: { ...SERVICE_PAYMENTS.task_definition, task_role_arn: '' } }
    renderWithProviders(<OverviewTab service={service} />)
    expect(valueOf('Task role')).toHaveTextContent(/^None$/)
  })

  it('Task role names the task role when there is one', () => {
    renderWithProviders(<OverviewTab service={SERVICE_PAYMENTS} />)
    expect(valueOf('Task role')).toHaveTextContent('payments-api-task')
  })
})
