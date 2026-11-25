import { renderHook, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { createElement, type ReactNode } from 'react'
import { MemoryRouter } from 'react-router'
import { describe, expect, it } from 'vitest'
import { AuthProvider } from '../app/auth/AuthProvider'
import { INVESTIGATION_DONE, mockInvestigations } from '../test/investigationFixtures'
import { useInvestigation, useServiceInvestigations } from './investigations'

function wrapper({ children }: { children: ReactNode }) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return createElement(QueryClientProvider, { client }, createElement(MemoryRouter, null, createElement(AuthProvider, null, children)))
}

describe('investigation hooks', () => {
  it('fetch the detail and the service list when the agent is on', async () => {
    const requested = mockInvestigations()
    const detail = renderHook(() => useInvestigation(INVESTIGATION_DONE.id), { wrapper })
    const list = renderHook(() => useServiceInvestigations(INVESTIGATION_DONE.subject.service_id), { wrapper })

    await waitFor(() => expect(detail.result.current.data?.id).toBe(INVESTIGATION_DONE.id))
    await waitFor(() => expect(list.result.current.data?.items).toHaveLength(1))
    expect(requested).toContain(`/api/investigations/${INVESTIGATION_DONE.id}`)
  })

  it('make no request when the agent is off', async () => {
    const requested = mockInvestigations([INVESTIGATION_DONE], false)
    const detail = renderHook(() => useInvestigation(INVESTIGATION_DONE.id), { wrapper })
    const list = renderHook(() => useServiceInvestigations(INVESTIGATION_DONE.subject.service_id), { wrapper })

    await new Promise((resolve) => setTimeout(resolve, 100))
    expect(detail.result.current.fetchStatus).toBe('idle')
    expect(list.result.current.fetchStatus).toBe('idle')
    expect(requested).toEqual([])
  })
})
