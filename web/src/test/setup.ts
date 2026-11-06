import '@testing-library/jest-dom/vitest'
import { cleanup, configure, render, type RenderResult } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { http, HttpResponse } from 'msw'
import { createElement, type ReactElement } from 'react'
import { MemoryRouter } from 'react-router'
import { afterAll, afterEach, beforeAll } from 'vitest'
import type { MeDTO } from '../api/types'
import { ToastProvider } from '../components/Toast'
import { server } from './msw'

// findBy*/waitFor give up after 1 s by default. The first render of a big page in a file (DeployTab, MetricsTab, the whole Shell)
// takes longer than that when the machine is busy, and the assertions then fail on timing, not on behaviour. A passing wait
// still returns as soon as its condition holds, so only a genuine failure takes longer to report.
configure({ asyncUtilTimeout: 5000 })

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
