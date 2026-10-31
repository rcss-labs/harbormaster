import { afterEach, describe, expect, it, vi } from 'vitest'
import { http, HttpResponse } from 'msw'
import { server } from '../test/msw'
import { api, apiFetch, ApiError, setCsrfToken } from './client'

afterEach(() => setCsrfToken(''))

describe('apiFetch', () => {
  it('adds the CSRF header on POST only', async () => {
    const seen: Record<string, string | null> = {}
    server.use(
      http.get('/api/probe', ({ request }) => {
        seen.get = request.headers.get('X-CSRF-Token')
        return HttpResponse.json({})
      }),
      http.post('/api/probe', ({ request }) => {
        seen.post = request.headers.get('X-CSRF-Token')
        return HttpResponse.json({})
      }),
    )
    setCsrfToken('tok-123')
    await apiFetch('/api/probe')
    await api.post('/api/probe', { a: 1 })
    expect(seen.get).toBeNull()
    expect(seen.post).toBe('tok-123')
  })

  it('throws ApiError with code from the body', async () => {
    server.use(
      http.post('/api/boom', () =>
        HttpResponse.json({ error: { code: 'conflict', message: 'tag already exists.' } }, { status: 409 }),
      ),
    )
    const err = await api.post('/api/boom').catch((e: unknown) => e)
    expect(err).toBeInstanceOf(ApiError)
    expect(err).toMatchObject({ code: 'conflict', status: 409, message: 'tag already exists.' })
  })

  it('dispatches hm:unauthenticated on 401', async () => {
    server.use(
      http.get('/api/me', () =>
        HttpResponse.json({ error: { code: 'unauthenticated', message: 'Sign in.' } }, { status: 401 }),
      ),
    )
    const listener = vi.fn()
    window.addEventListener('hm:unauthenticated', listener)
    await expect(apiFetch('/api/me')).rejects.toBeInstanceOf(ApiError)
    window.removeEventListener('hm:unauthenticated', listener)
    expect(listener).toHaveBeenCalledTimes(1)
  })
})
