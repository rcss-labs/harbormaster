import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { StackBar } from './StackBar'

const parts = [
  { n: 2, color: 'var(--ok)', label: 'Healthy' },
  { n: 1, color: 'var(--bad)', label: 'Unhealthy' },
  { n: 0, color: 'var(--mute)', label: 'Missing' },
]

describe('StackBar', () => {
  it('names the counts for assistive tech', () => {
    render(<StackBar parts={parts} label="Target health" />)
    expect(screen.getByRole('img', { name: 'Healthy 2, Unhealthy 1, Missing 0 of 3' })).toBeInTheDocument()
  })

  it('draws a segment only for parts with n > 0, each with a minimum width', () => {
    render(<StackBar parts={parts} label="Target health" />)
    const segments = screen.getByRole('img').querySelectorAll('i')
    expect(segments).toHaveLength(2)
    for (const segment of segments) expect((segment as HTMLElement).style.minWidth).toBe('2px')
  })

  it('sizes segments by their share of total, which can exceed the sum', () => {
    render(<StackBar parts={[{ n: 1, color: 'var(--ok)', label: 'Up' }]} total={4} label="Tasks" />)
    expect(screen.getByRole('img', { name: 'Up 1 of 4' })).toBeInTheDocument()
    const segment = screen.getByRole('img').querySelector('i') as HTMLElement
    expect(segment.style.width).toBe('25%')
  })

  it('lists the parts with counts when legend is set', () => {
    render(<StackBar parts={parts} label="Target health" legend />)
    const legend = screen.getByRole('list', { name: 'Target health' })
    expect(legend).toHaveTextContent('Healthy')
    expect(legend).toHaveTextContent('2')
    expect(legend).toHaveTextContent('Unhealthy')
  })

  it('has no legend by default', () => {
    render(<StackBar parts={parts} label="Target health" />)
    expect(screen.queryByRole('list')).not.toBeInTheDocument()
  })
})
