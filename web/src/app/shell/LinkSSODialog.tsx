import { useMutation } from '@tanstack/react-query'
import { useState } from 'react'
import { api } from '../../api/client'
import { Dialog } from '../../components/Dialog'
import { navigateExternal } from './navigateExternal'
import styles from './shell.module.css'

/** Only http(s) is a valid identity provider destination; javascript: and data: URLs must never be navigated to. */
function isWebUrl(url: string): boolean {
  try {
    const { protocol } = new URL(url, window.location.origin)
    return protocol === 'http:' || protocol === 'https:'
  } catch {
    return false
  }
}

/** Re-checks the current password, then sends the browser to the identity provider URL the server returns. */
export function LinkSSODialog({ onClose }: { onClose: () => void }) {
  const [password, setPassword] = useState('')
  const [error, setError] = useState('')

  const start = useMutation({
    mutationFn: () => api.post<{ redirect_url: string }>('/api/auth/saml/link', { password }),
    onSuccess: (result) => {
      if (!isWebUrl(result.redirect_url)) {
        setError('Could not start single sign-on. Try again or contact an administrator.')
        return
      }
      navigateExternal(result.redirect_url)
    },
    onError: (caught) => setError(caught.message),
  })

  const submit = () => {
    setError('')
    start.mutate()
  }

  return (
    <Dialog
      title="Link your SSO account"
      sub="You will sign in at your identity provider; the account you sign in with there is attached to this Harbormaster user. Enter your password to continue."
      onClose={onClose}
      actions={[
        { label: 'Cancel', onClick: onClose },
        { label: 'Continue to identity provider', kind: 'primary', onClick: submit, disabled: start.isPending || password === '' },
      ]}
    >
      <form
        onSubmit={(event) => {
          event.preventDefault()
          if (password !== '' && !start.isPending) submit()
        }}
      >
        <label className={styles.dialogField}>
          <span>Password</span>
          <input type="password" autoComplete="current-password" value={password} onChange={(event) => setPassword(event.target.value)} />
        </label>
        <div className={styles.dialogError} role="alert">
          {error}
        </div>
      </form>
    </Dialog>
  )
}
