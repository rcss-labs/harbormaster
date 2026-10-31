import { render, type RenderResult } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter, useLocation } from 'react-router'
import { AuthProvider } from '../app/auth/AuthProvider'
import { AppRoutes } from '../app/router'
import { ToastProvider } from '../components/Toast'

function LocationProbe() {
  const location = useLocation()
  return <output data-testid="location">{location.pathname}</output>
}

/** Renders the real AuthProvider and routes at `route`; a hidden probe exposes the current pathname. */
export function renderApp(route = '/'): RenderResult {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter initialEntries={[route]}>
        <ToastProvider>
          <AuthProvider>
            <AppRoutes />
            <LocationProbe />
          </AuthProvider>
        </ToastProvider>
      </MemoryRouter>
    </QueryClientProvider>,
  )
}
