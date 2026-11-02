import { useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { useLocation, useNavigate, useSearchParams } from 'react-router'
import { api, ApiError } from '../../api/client'
import { useAuthOptions } from '../../api/hooks'
import { useAuth } from './AuthProvider'
import { AuthCard, Field } from './AuthCard'
import styles from './auth.module.css'

const SSO_ERRORS: Record<string, string> = {
  sso_failed: 'Single sign-on failed. Try again, or ask a global admin to check the audit log.',
  sso_declined: 'Your identity provider declined the sign-in.',
  sso_off: 'Single sign-on is turned off.',
}

// return_to must be a same-origin relative path; the server re-checks, this only avoids sending junk.
const safeReturnPath = (path: string) => (path.startsWith('/') && !path.startsWith('//') && !path.includes('\\') ? path : '/')

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
  const [showPassword, setShowPassword] = useState(false)
  const [params] = useSearchParams()
  const options = useAuthOptions()
  const sso = options.data?.sso?.enabled ? options.data.sso : null
  const queryError = SSO_ERRORS[params.get('error') ?? ''] ?? ''

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

  if (options.isPending) return <AuthCard title="Sign in" busy={false} onSubmit={() => {}}>{null}</AuthCard>

  if (!sso) {
    return (
      <AuthCard title="Sign in" error={error || queryError} submitLabel="Sign in" busy={busy} onSubmit={submit}>
        <Field label="Username" value={username} onChange={setUsername} autoComplete="username" />
        <Field label="Password" value={password} onChange={setPassword} type="password" autoComplete="current-password" />
      </AuthCard>
    )
  }

  const signedOut = params.get('signed_out') === '1'
  return (
    <AuthCard
      title="Sign in"
      error={error || queryError}
      info={signedOut ? 'You are signed out of Harbormaster. To sign out of your identity provider, sign out there.' : undefined}
      submitLabel={showPassword ? 'Sign in' : undefined}
      busy={busy}
      onSubmit={submit}
    >
      <a className={`btn primary ${styles.ssoBtn}`} href={`/api/auth/saml/login?return_to=${encodeURIComponent(safeReturnPath(intended))}`}>
        {sso.label}
      </a>
      {showPassword ? (
        <>
          <span className={styles.hint}>Only break-glass accounts can sign in with a password.</span>
          <Field label="Username" value={username} onChange={setUsername} autoComplete="username" />
          <Field label="Password" value={password} onChange={setPassword} type="password" autoComplete="current-password" />
        </>
      ) : (
        <button type="button" className={styles.linkBtn} onClick={() => setShowPassword(true)}>
          Sign in with a password instead
        </button>
      )}
    </AuthCard>
  )
}
