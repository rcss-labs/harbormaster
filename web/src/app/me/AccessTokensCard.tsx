import { useState } from 'react'
import { useRevokeToken, useTokens, isTokensOff, type TokenDTO } from '../../api/tokens'
import { Ago, useNow } from '../../components/Ago'
import { ConfirmDialog } from '../../components/ConfirmDialog'
import { Panel } from '../../components/Panel'
import { useToast } from '../../components/Toast'
import { formatBytes } from '../logsearch/model'
import { CreateTokenDialog } from './CreateTokenDialog'
import styles from './myAccess.module.css'

const SOON_MS = 3 * 24 * 60 * 60 * 1000

/** Shared with the admin dialog: when a token expires, flagged inside 3 days. */
export function ExpiryText({ expiresAt }: { expiresAt: string }) {
  const now = useNow(60_000)
  const remaining = Date.parse(expiresAt) - now
  const date = new Date(expiresAt).toLocaleDateString()
  if (remaining <= 0) return <span className={styles.soon}>Expired {date}</span>
  const days = Math.max(1, Math.ceil(remaining / (24 * 60 * 60 * 1000)))
  const relative = `in ${days} ${days === 1 ? 'day' : 'days'}`
  return remaining < SOON_MS ? (
    <span className={styles.soon} title={date}>
      Expires {relative}
    </span>
  ) : (
    <span title={date}>Expires {relative}</span>
  )
}

/** Where a token was last used, or that it never was. */
export function LastUsedText({ token }: { token: TokenDTO }) {
  if (!token.last_used_at) return <span>Never used</span>
  return (
    <span>
      Last used <Ago ts={token.last_used_at} />
      {token.last_used_ip ? ` from ${token.last_used_ip}` : ''}
    </span>
  )
}

/** The signed-in user's personal access tokens. Renders nothing while tokens are off (the API answers 404). */
export function AccessTokensCard() {
  const tokens = useTokens()
  const revoke = useRevokeToken()
  const toast = useToast()
  const [creating, setCreating] = useState(false)
  const [revoking, setRevoking] = useState<TokenDTO | null>(null)

  if (tokens.isPending || tokens.isError) {
    // Off (404) hides the card; any other failure is shown in place so it is not mistaken for "off".
    if (tokens.isError && !isTokensOff(tokens.error)) {
      return (
        <Panel title="Access tokens">
          <div className={styles.err} role="alert">
            {tokens.error.message}
          </div>
        </Panel>
      )
    }
    return null
  }

  const { items, policy } = tokens.data
  const atLimit = items.length >= policy.max_tokens
  const budget =
    policy.log_search_daily_bytes > 0
      ? `Log search by tokens today: ${formatBytes(policy.log_search_used_bytes)} of ${formatBytes(policy.log_search_daily_bytes)}.`
      : null
  const connect =
    policy.mcp_url && policy.oauth
      ? `In claude.ai: Settings, Connectors, Add custom connector, paste ${policy.mcp_url}/mcp. Approve from a device on the company network.`
      : null
  const footer =
    budget || connect ? (
      <>
        {connect && <div>{connect}</div>}
        {budget && <div>{budget}</div>}
      </>
    ) : undefined

  const confirmRevoke = () => {
    if (!revoking) return
    const target = revoking
    setRevoking(null)
    revoke.mutate(target.id, {
      onSuccess: () => toast.show(`Revoked ${target.name}`, 'ok'),
      onError: (error) => toast.show(error.message, 'bad'),
    })
  }

  return (
    <>
      <Panel
        title="Access tokens"
        flush
        actions={
          <button type="button" className={`${styles.btn} ${styles.sm}`} disabled={atLimit} title={atLimit ? `You already have ${policy.max_tokens} tokens. Revoke one first.` : undefined} onClick={() => setCreating(true)}>
            Create token
          </button>
        }
        footer={footer}
      >
        {items.length === 0 ? (
          <div className={styles.empty}>No tokens. Create one to let Claude Code or another client read what you can see.</div>
        ) : (
          <div className={styles.tokenList}>
            {items.map((token) => (
              <div key={token.id} className={styles.tokenRow}>
                <div className={styles.tokenMain}>
                  <span className={styles.tokenName}>{token.name}</span>
                  <span className={`${styles.tokenMeta} ${styles.mono}`}>{token.public_id}</span>
                  <span className={styles.tokenMeta}>
                    Created {new Date(token.created_at).toLocaleDateString()} · <ExpiryText expiresAt={token.expires_at} />
                  </span>
                  <span className={styles.tokenMeta}>
                    <LastUsedText token={token} />
                  </span>
                </div>
                <button type="button" className={`${styles.btn} ${styles.sm} ${styles.danger}`} aria-label={`Revoke ${token.name}`} onClick={() => setRevoking(token)}>
                  Revoke
                </button>
              </div>
            ))}
          </div>
        )}
      </Panel>
      {creating && <CreateTokenDialog policy={policy} onClose={() => setCreating(false)} />}
      {revoking && (
        <ConfirmDialog
          danger
          title={`Revoke ${revoking.name}?`}
          sub="Anything using this token stops working at once. This cannot be undone."
          confirmLabel="Revoke token"
          onConfirm={confirmRevoke}
          onCancel={() => setRevoking(null)}
        />
      )}
    </>
  )
}
