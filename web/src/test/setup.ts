import '@testing-library/jest-dom/vitest'
import { cleanup, render, type RenderResult } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { http, HttpResponse } from 'msw'
import { createElement, type ReactElement } from 'react'
import { MemoryRouter } from 'react-router'
import { afterAll, afterEach, beforeAll } from 'vitest'
import type { MeDTO } from '../api/types'
import { ToastProvider } from '../components/Toast'
import { server } from './msw'

beforeAll(() => server.listen({ onUnhandledFrame: 'error' }))
afterEach(() => {
  cleanup()
  server.resetHandlers()
})
afterAll(() => server.close())

interface RenderOptions {
  /** Initial router location; defaults to "/". */
  route?: string
  /** Signed-in user returned by GET /api/me; defaults to the admin fixture. */
  me?: MeDTO
}

/** Renders `ui` with a fresh QueryClient (no retries) and a MemoryRouter at `route`. */
export function renderWithProviders(ui: ReactElement, { route = '/', me }: RenderOptions = {}): RenderResult {
  if (me) server.use(http.get('/api/me', () => HttpResponse.json(me)))
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    createElement(
      QueryClientProvider,
      { client: queryClient },
      createElement(MemoryRouter, { initialEntries: [route] }, createElement(ToastProvider, null, ui)),
    ),
  )
}
