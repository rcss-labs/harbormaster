import { screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { TAG_PROD } from '../../test/fixtures'
import { renderWithProviders } from '../../test/setup'
import { BarCell, MonoCell, ServiceLink, TagCell } from './cells'

describe('analytics cells', () => {
  it('BarCell width = pct%', () => {
    const { container } = renderWithProviders(<BarCell pct={37.5} label="37.5%" color="red" />)
    expect(screen.getByText('37.5%')).toBeInTheDocument()
    expect(container.querySelector('[data-bar]')).toHaveStyle({ width: '37.5%' })
  })

  it('BarCell clamps to 0-100', () => {
    const { container } = renderWithProviders(<BarCell pct={140} label="x" />)
    expect(container.querySelector('[data-bar]')).toHaveStyle({ width: '100%' })
  })

  it('MonoCell shows its text', () => {
    renderWithProviders(<MonoCell>2.4 s</MonoCell>)
    expect(screen.getByText('2.4 s')).toBeInTheDocument()
  })

  it('ServiceLink goes to the service page', () => {
    renderWithProviders(<ServiceLink id="svc-1" name="payments-api" cluster="core" />)
    expect(screen.getByRole('link', { name: 'payments-api' })).toHaveAttribute('href', '/services/svc-1')
    expect(screen.getByText('core')).toBeInTheDocument()
  })

  it('TagCell renders the tag chip', () => {
    renderWithProviders(<TagCell tag={TAG_PROD} />)
    expect(screen.getByText(TAG_PROD.name)).toBeInTheDocument()
  })
})
