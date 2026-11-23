import { useEffect, useMemo, useState } from 'react'
import { useLocation } from 'react-router'
import { authorizeRequestFrom, oauthErrorOf, safeRedirect, useAuthorizeDecision, useAuthorizeRequest } from '../../api/oauth'
import { AuthCard } from './AuthCard'
import styles from './auth.module.css'

/** Sends the browser back to the application. Only ever called with an address the server validated. */
function leaveTo(target: string) {
  window.location.assign(target)
}

/**
 * The OAuth consent page (/oauth/authorize). It reads the request from the URL, asks the server to validate it, and
 * shows either an error or what the application asks for, with Approve and Deny. An error the server did not mark as
 * redirectable is shown here and nothing else happens: no button, no redirect.
 */
export function OAuthConsent() {
  const location = useLocation()
  const request = useMemo(() => authorizeRequestFrom(location.search), [location.search])
  const validation = useAuthorizeRequest(request)
  const decision = useAuthorizeDecision()
  const [leaving, setLeaving] = useState(false)
  const [decisionError, setDecisionError] = useState('')

  const validationError = validation.isError ? oauthErrorOf(validation.error) : null
  const errorRedirect = safeRedirect(validationError?.redirect_to)

  // A redirectable error belongs to the application: send the browser back with it.
  useEffect(() => {
    if (errorRedirect) leaveTo(errorRedirect)
  }, [errorRedirect])

  const decide = (choice: 'approve' | 'deny') => {
    setDecisionError('')
    decision.mutate(
      { request, decision: choice },
      {
        onSuccess: (answer) => {
          const target = safeRedirect(answer.redirect_to)
          if (!target) {
            setDecisionError('Harbormaster did not return a valid address for the application.')
            return
          }
          setLeaving(true)
          leaveTo(target)
        },
        onError: (error) => {
          const refused = oauthErrorOf(error)
          const target = safeRedirect(refused.redirect_to)
          if (target) {
            setLeaving(true)
            leaveTo(target)
            return
          }
          setDecisionError(refused.error_description)
        },
      },
    )
  }

  if (validation.isPending) {
    return (
      <AuthCard title="Connect an application" busy={false} onSubmit={() => {}}>
        <p className={styles.sub}>Checking the request…</p>
      </AuthCard>
    )
  }

  if (validationError) {
    if (errorRedirect) {
      return (
        <AuthCard title="Connect an application" busy={false} onSubmit={() => {}}>
          <p className={styles.sub}>Returning to the application…</p>
        </AuthCard>
      )
    }
    return (
      <AuthCard title="This request cannot be approved" busy={false} onSubmit={() => {}}>
        <div className={styles.err} role="alert">
          {validationError.error_description}
        </div>
        <p className={styles.sub}>Close this tab.</p>
      </AuthCard>
    )
  }

  const view = validation.data
  if (!view) return null
  const busy = decision.isPending || leaving
  return (
    <AuthCard
      title={`Connect ${view.client_name}?`}
      sub={`${view.client_name} (${view.client_host}) asks to use Harbormaster on your behalf.`}
      error={decisionError}
      submitLabel="Approve"
      busy={busy}
      onSubmit={() => decide('approve')}
    >
      <p className={styles.sub}>
        <strong>Read-only access to Harbormaster as {view.username}.</strong> It cannot change, deploy, scale or open a terminal.
      </p>
      <p className={styles.sub}>
        It will see everything you can see{view.sees_logs ? ', including logs' : ''}.
      </p>
      <p className={styles.sub}>
        Access lasts up to {view.expires_in_days} {view.expires_in_days === 1 ? 'day' : 'days'}; you can revoke it on My access.
      </p>
      <p className={styles.sub}>
        <strong>
          Only approve if you started this from your own {view.client_name} just now. If someone sent you this link, deny it.
        </strong>
      </p>
      <p className={styles.hint}>After you decide, you return to {view.redirect_host}.</p>
      <button type="button" className={`btn ${styles.submit}`} disabled={busy} onClick={() => decide('deny')}>
        Deny
      </button>
    </AuthCard>
  )
}
