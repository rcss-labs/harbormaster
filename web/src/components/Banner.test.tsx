import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { Banner } from './Banner'

describe('Banner', () => {
  it('shows its message and carries the data-hm-banner marker', () => {
    const { container } = render(<Banner kind="warn">AWS is slow.</Banner>)
    expect(screen.getByText('AWS is slow.')).toBeInTheDocument()
    expect(container.querySelector('[data-hm-banner]')).not.toBeNull()
  })

  it('renders an action after the message', () => {
    render(
      <Banner kind="bad" action={<button type="button">Open AWS accounts</button>}>
        Credentials failed.
      </Banner>,
    )
    expect(screen.getByRole('button', { name: 'Open AWS accounts' })).toBeInTheDocument()
  })

  it('compact adds the compact class', () => {
    const { container } = render(
      <Banner kind="info" compact>
        Quiet.
      </Banner>,
    )
    expect(container.querySelector('[data-hm-banner]')).toHaveClass('compact')
  })
})
