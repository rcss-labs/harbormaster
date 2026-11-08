import { screen } from '@testing-library/react'
import { beforeEach, describe, expect, it } from 'vitest'
import { renderApp } from '../test/renderApp'

beforeEach(() => {
  localStorage.clear()
  window.history.replaceState(null, '', '/')
})

describe('router', () => {
  it('/services renders the Services page ahead of the service detail route', async () => {
    renderApp('/services')
    expect(await screen.findByRole('heading', { level: 1, name: 'Services' })).toBeInTheDocument()
    expect(await screen.findByRole('table', { name: 'Services' })).toBeInTheDocument()
  })
})
