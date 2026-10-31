import { QueryObserver, type QueryClient } from '@tanstack/react-query'
import { describe, expect, it, vi } from 'vitest'
import { http, HttpResponse } from 'msw'
import { server } from '../test/msw'
import { api } from './client'
import { createQueryClient } from './queryClient'

function countingEndpoint(status: number) {
  const counter = { requests: 0 }
  server.use(
    http.get('/api/probe', () => {
      counter.requests += 1
      return HttpResponse.json({ error: { code: 'x', message: 'nope' } }, { status })
    }),
  )
  return counter
}

// Observers (what useQuery uses) apply the retry option; fetchQuery would not.
function settleWithError(client: QueryClient): Promise<unknown> {
  const observer = new QueryObserver(client, { queryKey: ['probe'], queryFn: () => api.get('/api/probe') })
  return new Promise((resolve) => {
    const unsubscribe = observer.subscribe((result) => {
      if (result.status === 'error') {
        unsubscribe()
        resolve(result.error)
      }
    })
  })
}

describe('createQueryClient retry policy', () => {
  it('does not retry a 404 (one request)', async () => {
    const counter = countingEndpoint(404)
    const client = createQueryClient({ retryDelay: 0 })
    await expect(settleWithError(client)).resolves.toMatchObject({ status: 404 })
    expect(counter.requests).toBe(1)
  })

  it('fires hm:unauthenticated once for a 401', async () => {
    const counter = countingEndpoint(401)
    const onUnauthenticated = vi.fn()
    window.addEventListener('hm:unauthenticated', onUnauthenticated)
    try {
      const client = createQueryClient({ retryDelay: 0 })
      await settleWithError(client)
    } finally {
      window.removeEventListener('hm:unauthenticated', onUnauthenticated)
    }
    expect(counter.requests).toBe(1)
    expect(onUnauthenticated).toHaveBeenCalledTimes(1)
  })

  it('retries a 5xx a small number of times', async () => {
    const counter = countingEndpoint(503)
    const client = createQueryClient({ retryDelay: 0 })
    await expect(settleWithError(client)).resolves.toMatchObject({ status: 503 })
    expect(counter.requests).toBe(3)
  })
})
