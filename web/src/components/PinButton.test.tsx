import { render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { installPhoneMedia } from '../test/phone'
import { PinButton } from './PinButton'

describe('PinButton', () => {
  let restore: (() => void) | undefined
  afterEach(() => restore?.())

  it('names the sidebar on desktop', () => {
    restore = installPhoneMedia(false)
    render(<PinButton name="api" pinned={false} onToggle={() => {}} />)
    expect(screen.getByRole('button', { name: 'Pin api' })).toHaveAttribute('title', 'Pin to the sidebar')
  })

  it('does not mention a sidebar on phones, which have none', () => {
    restore = installPhoneMedia(true)
    render(<PinButton name="api" pinned onToggle={() => {}} />)
    expect(screen.getByRole('button', { name: 'Unpin api' })).toHaveAttribute('title', 'Unpin from Overview and Services')
  })
})
