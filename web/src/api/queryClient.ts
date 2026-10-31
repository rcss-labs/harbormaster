import { QueryClient } from '@tanstack/react-query'
import { ApiError } from './client'

const MAX_RETRIES = 2

// A 4xx is a definite answer (no access, session expired, validation): retrying it only delays the
// UI and repeats side effects such as the hm:unauthenticated event. Network and 5xx errors get a
// couple of retries.
export function shouldRetryQuery(failureCount: number, error: unknown): boolean {
  if (error instanceof ApiError && error.status >= 400 && error.status < 500) return false
  return failureCount < MAX_RETRIES
}

export function createQueryClient(options: { retryDelay?: number } = {}): QueryClient {
  return new QueryClient({
    defaultOptions: { queries: { retry: shouldRetryQuery, retryDelay: options.retryDelay } },
  })
}
