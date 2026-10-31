import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { QueryClient } from '@tanstack/react-query'
import { act } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { ME_ADMIN } from '../../test/fixtures'
import { server } from '../../test/msw'
import { renderApp } from '../../test/renderApp'

const unauthenticated = () =>
  HttpResponse.json({ error: { code: 'unauthenticated', message: 'Sign in required.' } }, { status: 401 })

describe('Login', () => {
  it('shows generic error on 401', async () => {
    server.use(
      http.get('/api/me', unauthenticated),
      http.post('/api/auth/login', () =>
        HttpResponse.json({ error: { code: 'unauthenticated', message: 'Wrong username or password.' } }, { status: 401 }),
      ),
    )
    renderApp('/login')
    const user = userEvent.setup()
    await user.type(await screen.findByLabelText('Username'), 'pavel')
    await user.type(screen.getByLabelText('Password'), 'nope')
    await user.click(screen.getByRole('button', { name: 'Sign in' }))
    expect(await screen.findByText('Wrong username or password.')).toBeInTheDocument()
  })

  it('navigates to intended path after success', async () => {
    let signedIn = false
    server.use(
      http.get('/api/me', () => (signedIn ? HttpResponse.json(ME_ADMIN) : unauthenticated())),
      http.post('/api/auth/login', () => {
        signedIn = true
        return HttpResponse.json(ME_ADMIN)
      }),
    )
    renderApp('/clusters')
    const user = userEvent.setup()
    await user.type(await screen.findByLabelText('Username'), 'pavel')
    await user.type(screen.getByLabelText('Password'), 'correct horse battery')
    expect(screen.getByTestId('location')).toHaveTextContent('/login')
    await user.click(screen.getByRole('button', { name: 'Sign in' }))
    await waitFor(() => expect(screen.getByTestId('location')).toHaveTextContent('/clusters'))
  })
})

describe('Session end', () => {
  it('drops cached data when the session expires', async () => {
    let signedIn = true
    server.use(
      http.get('/api/me', () => (signedIn ? HttpResponse.json(ME_ADMIN) : unauthenticated())),
      http.post('/api/auth/login', () => {
        signedIn = true
        return HttpResponse.json(ME_ADMIN)
      }),
    )
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    queryClient.setQueryData(['clusters'], { items: ['user A data'] })
    renderApp('/', queryClient)
    expect(await screen.findByRole('heading', { name: 'Overview' })).toBeInTheDocument()

    signedIn = false
    act(() => {
      window.dispatchEvent(new Event('hm:unauthenticated'))
    })
    const user = userEvent.setup()
    await user.type(await screen.findByLabelText('Username'), 'omer')
    await user.type(screen.getByLabelText('Password'), 'correct horse battery')
    expect(queryClient.getQueryData(['clusters'])).toBeUndefined()
    queryClient.setQueryData(['clusters'], { items: ['user A data'] })
    await user.click(screen.getByRole('button', { name: 'Sign in' }))
    await screen.findByRole('heading', { name: 'Overview' })
    expect(queryClient.getQueryData(['clusters'])).toBeUndefined()
  })
})

describe('Router', () => {
  it('redirects to /setup when setup is needed', async () => {
    server.use(
      http.get('/api/me', unauthenticated),
      http.get('/api/auth/setup', () => HttpResponse.json({ needed: true })),
    )
    renderApp('/')
    expect(await screen.findByRole('heading', { name: 'Create the first global admin' })).toBeInTheDocument()
    expect(screen.getByTestId('location')).toHaveTextContent('/setup')
  })

  it('blocks /clusters while must_change_password and shows ChangePassword', async () => {
    server.use(http.get('/api/me', () => HttpResponse.json({ ...ME_ADMIN, must_change_password: true })))
    renderApp('/clusters')
    expect(await screen.findByRole('heading', { name: 'Choose a new password' })).toBeInTheDocument()
    expect(screen.getByText('Your password is temporary. Set a new one to continue.')).toBeInTheDocument()
    expect(screen.getByTestId('location')).toHaveTextContent('/password')
  })
})

describe('Setup', () => {
  it('posts token, username, password', async () => {
    let posted: unknown
    server.use(
      http.get('/api/me', unauthenticated),
      http.post('/api/auth/setup', async ({ request }) => {
        posted = await request.json()
        return HttpResponse.json(ME_ADMIN)
      }),
    )
    renderApp('/setup')
    const user = userEvent.setup()
    await user.type(await screen.findByLabelText('Setup token'), 'tok-123')
    await user.type(screen.getByLabelText('Username'), 'root')
    await user.type(screen.getByLabelText('Password'), 'a-long-enough-password')
    await user.type(screen.getByLabelText('Confirm'), 'a-long-enough-password')
    await user.click(screen.getByRole('button', { name: 'Create admin' }))
    await waitFor(() => expect(posted).toEqual({ token: 'tok-123', username: 'root', password: 'a-long-enough-password' }))
  })
})
