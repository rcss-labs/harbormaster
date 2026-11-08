import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { Icon } from './Icon'
import { ICONS, type IconName } from './icons'

describe('Icon', () => {
  it('renders a path with the d of the named icon', () => {
    const { container } = render(<Icon name="search" />)
    expect(container.querySelector('path')).toHaveAttribute('d', ICONS.search)
  })

  it('is hidden from assistive tech without a label', () => {
    const { container } = render(<Icon name="search" />)
    expect(container.querySelector('svg')).toHaveAttribute('aria-hidden', 'true')
    expect(screen.queryByRole('img')).not.toBeInTheDocument()
  })

  it('exposes role img and an accessible name with a label', () => {
    render(<Icon name="warn" label="Warning" />)
    const icon = screen.getByRole('img', { name: 'Warning' })
    expect(icon).not.toHaveAttribute('aria-hidden')
  })

  it('sizes the svg and passes className through', () => {
    const { container } = render(<Icon name="x" size={12} className="mine" />)
    const svg = container.querySelector('svg')
    expect(svg).toHaveAttribute('width', '12')
    expect(svg).toHaveAttribute('height', '12')
    expect(svg).toHaveClass('mine')
  })

  it('has the 45 icons of the mockup set, and an unknown name is a type error', () => {
    expect(Object.keys(ICONS)).toHaveLength(45)
    // @ts-expect-error not part of the icon set
    const unknown: IconName = 'not-an-icon'
    expect(unknown).toBe('not-an-icon')
  })
})
