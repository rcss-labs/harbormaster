import { useState } from 'react'
import { useRevokeUserToken, useUserTokens, type TokenDTO } from '../../../api/tokens'
import type { UserDTO } from '../../../api/types'
import { ConfirmDialog } from '../../../components/ConfirmDialog'
import { Dialog } from '../../../components/Dialog'
import { useToast } from '../../../components/Toast'
import { ExpiryText, LastUsedText } from '../../me/AccessTokensCard'
import styles from './users.module.css'

/** Admin view of one user's personal access tokens, with revoke. */
export function UserTokensDialog({ user, onClose }: { user: UserDTO; onClose: () => void }) {
  const tokens = useUserTokens(user.id)
  const revoke = useRevokeUserToken()
  const toast = useToast()
  const [revoking, setRevoking] = useState<TokenDTO | null>(null)

  const confirmRevoke = () => {
    if (!revoking) return
    const target = revoking
    setRevoking(null)
    revoke.mutate(
      { userId: user.id, tokenId: target.id },
      {
        onSuccess: () => toast.show(`Revoked ${target.name}`, 'ok'),
        onError: (error) => toast.show(error.message, 'bad'),
      },
    )
  }

  if (revoking) {
    return (
      <ConfirmDialog
        danger
        title={`Revoke ${revoking.name}?`}
        sub={`Anything using this token of ${user.username} stops working at once. This cannot be undone.`}
        confirmLabel="Revoke token"
        onConfirm={confirmRevoke}
        onCancel={() => setRevoking(null)}
      />
    )
  }

  return (
    <Dialog title={`Access tokens of ${user.username}`} wide actions={[{ label: 'Close', kind: 'primary', onClick: onClose }]} onClose={onClose}>
      {tokens.isPending ? (
        <p className={styles.hint}>Loading…</p>
      ) : tokens.isError ? (
        <div className={styles.err} role="alert">
          {tokens.error.message}
        </div>
      ) : tokens.data.items.length === 0 ? (
        <p className={styles.hint}>{user.username} has no tokens.</p>
      ) : (
        <div className={styles.stack}>
          {tokens.data.items.map((token) => (
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
              <button type="button" className={`${styles.btn} ${styles.sm} ${styles.danger}`} aria-label={`Revoke ${token.name}`} onClick={() => setRevoking(token)}>
                Revoke
              </button>
            </div>
          ))}
        </div>
      )}
    </Dialog>
  )
}
