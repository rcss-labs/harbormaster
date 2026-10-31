import { useState } from 'react'
import { useNavigate } from 'react-router'
import { api, ApiError } from '../../api/client'
import { useAuth } from './AuthProvider'
import { AuthCard, Field } from './AuthCard'

export function ChangePassword() {
  const { me, refresh } = useAuth()
  const navigate = useNavigate()
  const forced = me?.must_change_password ?? false
  const [current, setCurrent] = useState('')
  const [next, setNext] = useState('')
  const [confirm, setConfirm] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  const submit = async () => {
    if (next !== confirm) {
      setError('Passwords do not match.')
      return
    }
    setBusy(true)
    setError('')
    try {
      await api.post('/api/auth/password', { current, new: next })
      await refresh()
      navigate('/', { replace: true })
    } catch (e) {
      if (e instanceof ApiError && e.status === 401) setError('Current password is wrong.')
      else setError(e instanceof ApiError ? e.message : 'Could not change the password.')
      setBusy(false)
    }
  }

  return (
    <AuthCard
      title="Choose a new password"
      sub={forced ? 'Your password is temporary. Set a new one to continue.' : 'Enter your current password and a new one.'}
      error={error}
      submitLabel="Change password"
      busy={busy}
      onSubmit={submit}
    >
      <Field label="Current password" value={current} onChange={setCurrent} type="password" autoComplete="current-password" />
      <Field label="New password" value={next} onChange={setNext} type="password" autoComplete="new-password" />
      <Field label="Confirm" value={confirm} onChange={setConfirm} type="password" autoComplete="new-password" />
    </AuthCard>
  )
}
