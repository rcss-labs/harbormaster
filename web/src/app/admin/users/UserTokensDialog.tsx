import { useState } from 'react'
import { useRevokeUserGrant } from '../../../api/oauth'
import { useRevokeUserToken, useUserTokens, type GrantDTO, type TokenDTO } from '../../../api/tokens'
import type { UserDTO } from '../../../api/types'
import { ConfirmDialog } from '../../../components/ConfirmDialog'
import { Dialog } from '../../../components/Dialog'
import { useToast } from '../../../components/Toast'
import { ExpiryText, LastUsedText } from '../../me/AccessTokensCard'
import { GrantLastUsed } from '../../me/ConnectedAppsCard'
import styles from './users.module.css'

type Revoking = { kind: 'token'; token: TokenDTO } | { kind: 'grant'; grant: GrantDTO }

/** Admin view of one user's personal access tokens and connected apps, with revoke. */
export function UserTokensDialog({ user, onClose }: { user: UserDTO; onClose: () => void }) {
  const tokens = useUserTokens(user.id)
  const revoke = useRevokeUserToken()
  const revokeGrant = useRevokeUserGrant()
  const toast = useToast()
  const [revoking, setRevoking] = useState<Revoking | null>(null)

  const confirmRevoke = () => {
    if (!revoking) return
    const target = revoking
    setRevoking(null)
    if (target.kind === 'token') {
      revoke.mutate(
        { userId: user.id, tokenId: target.token.id },
        {
          onSuccess: () => toast.show(`Revoked ${target.token.name}`, 'ok'),
          onError: (error) => toast.show(error.message, 'bad'),
        },
      )
      return
    }
    revokeGrant.mutate(
      { userId: user.id, grantId: target.grant.id },
      {
        onSuccess: () => toast.show(`Disconnected ${target.grant.client_name}`, 'ok'),
        onError: (error) => toast.show(error.message, 'bad'),
      },
    )
  }

  if (revoking) {
    const name = revoking.kind === 'token' ? revoking.token.name : revoking.grant.client_name
    return (
      <ConfirmDialog
        danger
        title={revoking.kind === 'token' ? `Revoke ${name}?` : `Disconnect ${name}?`}
        sub={`Anything using this ${revoking.kind === 'token' ? 'token' : 'connection'} of ${user.username} stops working at once. This cannot be undone.`}
        confirmLabel={revoking.kind === 'token' ? 'Revoke token' : 'Disconnect'}
        onConfirm={confirmRevoke}
        onCancel={() => setRevoking(null)}
      />
    )
  }

  const grants = tokens.data?.grants ?? []
  return (
    <Dialog title={`Access tokens of ${user.username}`} wide actions={[{ label: 'Close', kind: 'primary', onClick: onClose }]} onClose={onClose}>
      {tokens.isPending ? (
        <p className={styles.hint}>Loading…</p>
      ) : tokens.isError ? (
        <div className={styles.err} role="alert">
          {tokens.error.message}
        </div>
      ) : (
        <div className={styles.stack}>
          {tokens.data.items.length === 0 ? (
            <p className={styles.hint}>{user.username} has no tokens.</p>
          ) : (
            tokens.data.items.map((token) => (
              <div key={token.id} className={styles.tokenRow}>
                <div className={styles.tokenMain}>
                  <span className={styles.name}>{token.name}</span>
                  <span className={`${styles.muted} ${styles.mono}`}>{token.public_id}</span>
                  <span className={`${styles.muted} ${styles.small}`}>
                    Created {new Date(token.created_at).toLocaleDateString()} · <ExpiryText expiresAt={token.expires_at} />
                  </span>
                  <span className={`${styles.muted} ${styles.small}`}>
                    <LastUsedText token={token} />
                  </span>
                </div>
                <button type="button" className={`${styles.btn} ${styles.sm} ${styles.danger}`} aria-label={`Revoke ${token.name}`} onClick={() => setRevoking({ kind: 'token', token })}>
                  Revoke
                </button>
              </div>
            ))
          )}
          {grants.length > 0 && <p className={styles.hint}>Connected apps</p>}
          {grants.map((grant) => (
            <div key={`grant-${grant.id}`} className={styles.tokenRow}>
              <div className={styles.tokenMain}>
                <span className={styles.name}>{grant.client_name}</span>
                <span className={`${styles.muted} ${styles.mono}`}>{grant.client_id}</span>
                <span className={`${styles.muted} ${styles.small}`}>
                  Connected {new Date(grant.created_at).toLocaleDateString()} · <ExpiryText expiresAt={grant.expires_at} />
                </span>
                <span className={`${styles.muted} ${styles.small}`}>
                  <GrantLastUsed grant={grant} />
                </span>
              </div>
              <button type="button" className={`${styles.btn} ${styles.sm} ${styles.danger}`} aria-label={`Revoke ${grant.client_name}`} onClick={() => setRevoking({ kind: 'grant', grant })}>
                Revoke
              </button>
            </div>
          ))}
        </div>
      )}
    </Dialog>
  )
}
