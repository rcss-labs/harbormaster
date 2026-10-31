import { useState } from 'react'
import { useNavigate } from 'react-router'
import { api, ApiError } from '../../api/client'
import { useAuth } from './AuthProvider'
import { AuthCard, Field } from './AuthCard'

export function Setup() {
  const { refresh } = useAuth()
  const navigate = useNavigate()
  const [token, setToken] = useState('')
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [confirm, setConfirm] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  const submit = async () => {
    if (password !== confirm) {
      setError('Passwords do not match.')
      return
    }
    setBusy(true)
    setError('')
    try {
      await api.post('/api/auth/setup', { token, username, password })
      await refresh()
      navigate('/', { replace: true })
    } catch (e) {
      if (e instanceof ApiError && e.status === 403) setError('That setup token is wrong.')
      else setError(e instanceof ApiError ? e.message : 'Could not complete setup.')
      setBusy(false)
    }
  }

  return (
    <AuthCard title="Create the first global admin" error={error} submitLabel="Create admin" busy={busy} onSubmit={submit}>
      <Field label="Setup token" value={token} onChange={setToken} hint="Printed in the server log at start" autoComplete="off" />
      <Field label="Username" value={username} onChange={setUsername} autoComplete="username" />
      <Field label="Password" value={password} onChange={setPassword} type="password" autoComplete="new-password" />
      <Field label="Confirm" value={confirm} onChange={setConfirm} type="password" autoComplete="new-password" />
    </AuthCard>
  )
}
