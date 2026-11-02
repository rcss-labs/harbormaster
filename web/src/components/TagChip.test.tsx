import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import type { TagDTO } from '../api/types'
import { TagChip } from './TagChip'

const tag: TagDTO = { id: 1, name: 'Prod', color: 'red', inherited: false }

describe('TagChip variants', () => {
  it('can be forced dashed without being inherited', () => {
    const { rerender } = render(<TagChip tag={tag} />)
    const solid = screen.getByText('Prod').className
    rerender(<TagChip tag={tag} variant="dashed" />)
    expect(screen.getByText('Prod').className).not.toBe(solid)
    expect(screen.getByText('Prod')).toHaveAttribute('title', 'Tag set on this service')
  })

  it('can be forced solid even when inherited', () => {
    render(<TagChip tag={{ ...tag, inherited: true }} variant="solid" />)
    expect(screen.getByText('Prod').className).not.toMatch(/inh/)
  })
})
