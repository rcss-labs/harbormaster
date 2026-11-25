import { screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { INVESTIGATION_DONE, mockInvestigations } from '../../test/investigationFixtures'
import { renderApp } from '../../test/renderApp'

describe('InvestigationPage', () => {
  it('shows the investigation', async () => {
    mockInvestigations()
    renderApp(`/investigations/${INVESTIGATION_DONE.id}`)

    expect(await screen.findByText('CannotPullContainerError: manifest unknown')).toBeInTheDocument()
  })

  it('not found', async () => {
    mockInvestigations()
    renderApp('/investigations/999')

    expect(await screen.findByText('Page not found.')).toBeInTheDocument()
  })

  it('incident without investigation', async () => {
    mockInvestigations()
    renderApp('/investigations/incident/inc-none')

    expect(await screen.findByText('No investigation yet for this alert.')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Open services' })).toHaveAttribute('href', '/services')
  })

  it('incident with an investigation shows it', async () => {
    mockInvestigations([{ ...INVESTIGATION_DONE, id: 55 }])
    renderApp('/investigations/incident/55')

    expect(await screen.findByText('CannotPullContainerError: manifest unknown')).toBeInTheDocument()
  })

  it('is not found, with no request, when the agent is off', async () => {
    const requested = mockInvestigations([INVESTIGATION_DONE], false)
    renderApp(`/investigations/${INVESTIGATION_DONE.id}`)

    expect(await screen.findByText('Page not found.')).toBeInTheDocument()
    expect(requested).toEqual([])
  })
})
