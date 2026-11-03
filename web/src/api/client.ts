// Same-origin JSON client. Errors become ApiError; a 401 also tells the app shell to show the login screen.

let csrfToken = ''

export function setCsrfToken(token: string): void {
  csrfToken = token
}

export class ApiError extends Error {
  code: string
  status: number
  /** The parsed error response, for errors that carry more than a message (a 409 returns the fresh state). */
  body: unknown

  constructor(code: string, message: string, status: number, body?: unknown) {
    super(message)
    this.name = 'ApiError'
    this.code = code
    this.status = status
    this.body = body
  }
}

export async function apiFetch<T>(path: string, init: RequestInit = {}): Promise<T> {
  const method = (init.method ?? 'GET').toUpperCase()
  const headers = new Headers(init.headers)
  if (init.body !== undefined && !headers.has('Content-Type')) {
    headers.set('Content-Type', 'application/json')
  }
  if (method !== 'GET' && csrfToken) {
    headers.set('X-CSRF-Token', csrfToken)
  }

  const response = await fetch(path, { ...init, method, headers, credentials: 'same-origin' })

  if (!response.ok) {
    let code = 'unknown'
    let message = response.statusText || 'Request failed.'
    let parsed: unknown
    try {
      const body = (await response.json()) as { error?: { code?: string; message?: string } }
      parsed = body
      if (body.error?.code) code = body.error.code
      if (body.error?.message) message = body.error.message
    } catch {
      // Non-JSON error body: keep the defaults.
    }
    if (response.status === 401 && !path.startsWith('/api/auth/')) {
      window.dispatchEvent(new Event('hm:unauthenticated'))
    }
    throw new ApiError(code, message, response.status, parsed)
  }

  if (response.status === 204) return undefined as T
  const text = await response.text()
  return (text ? JSON.parse(text) : undefined) as T
}

const jsonBody = (body: unknown): RequestInit['body'] => (body === undefined ? undefined : JSON.stringify(body))

export const api = {
  get: <T>(path: string) => apiFetch<T>(path),
  post: <T>(path: string, body?: unknown) => apiFetch<T>(path, { method: 'POST', body: jsonBody(body) }),
  put: <T>(path: string, body: unknown) => apiFetch<T>(path, { method: 'PUT', body: jsonBody(body) }),
  patch: <T>(path: string, body: unknown) => apiFetch<T>(path, { method: 'PATCH', body: jsonBody(body) }),
  del: <T = void>(path: string) => apiFetch<T>(path, { method: 'DELETE' }),
}
