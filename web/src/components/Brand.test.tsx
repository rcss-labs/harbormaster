import { render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { Brand } from './Brand'

afterEach(() => {
  delete document.documentElement.dataset.theme
})

describe('Brand', () => {
  it('renders light symbol in sidebar by default', () => {
    document.documentElement.dataset.theme = 'light'
    render(<Brand variant="sidebar" />)
    expect(screen.getByRole('img', { name: 'Harbormaster' }).getAttribute('src')).toMatch(/\/brand\/harbormaster-symbol\.svg$/)
    expect(screen.getByText('Harbormaster')).toBeTruthy()
  })

  it('switches to the dark symbol under data-theme=dark', () => {
    document.documentElement.dataset.theme = 'dark'
    render(<Brand variant="sidebar" />)
    expect(screen.getByRole('img', { name: 'Harbormaster' }).getAttribute('src')).toMatch(/\/brand\/harbormaster-symbol-dark\.svg$/)
  })

  it('signin uses the lockup in light and symbol+wordmark in dark', () => {
    document.documentElement.dataset.theme = 'light'
    const { unmount } = render(<Brand variant="signin" />)
    expect(screen.getByRole('img', { name: 'Harbormaster' }).getAttribute('src')).toMatch(/\/brand\/harbormaster-lockup\.png$/)
    expect(screen.queryByText('Harbormaster')).toBeNull()
    unmount()

    document.documentElement.dataset.theme = 'dark'
    render(<Brand variant="signin" />)
    expect(screen.getByRole('img', { name: 'Harbormaster' }).getAttribute('src')).toMatch(/\/brand\/harbormaster-symbol-dark\.svg$/)
    expect(screen.getByText('Harbormaster')).toBeTruthy()
  })
})
