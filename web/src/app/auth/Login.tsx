import { useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { useLocation, useNavigate } from 'react-router'
import { api, ApiError } from '../../api/client'
import { useAuth } from './AuthProvider'
import { AuthCard, Field } from './AuthCard'

export function Login() {
  const { refresh } = useAuth()
  const queryClient = useQueryClient()
  const navigate = useNavigate()
  const location = useLocation()
  const intended = (location.state as { from?: string } | null)?.from ?? '/'
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  const submit = async () => {
    setBusy(true)
    setError('')
    try {
      await api.post('/api/auth/login', { username, password })
      queryClient.clear()
      await refresh()
      navigate(intended, { replace: true })
    } catch (e) {
      setError(e instanceof ApiError && e.status === 401 ? 'Wrong username or password.' : e instanceof ApiError ? e.message : 'Could not sign in.')
      setBusy(false)
    }
  }

  return (
    <AuthCard title="Sign in" error={error} submitLabel="Sign in" busy={busy} onSubmit={submit}>
      <Field label="Username" value={username} onChange={setUsername} autoComplete="username" />
      <Field label="Password" value={password} onChange={setPassword} type="password" autoComplete="current-password" />
    </AuthCard>
  )
}
