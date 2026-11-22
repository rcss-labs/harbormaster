import { useState } from 'react'
import { useRevokeGrant } from '../../api/oauth'
import { useTokens, type GrantDTO } from '../../api/tokens'
import { Ago } from '../../components/Ago'
import { ConfirmDialog } from '../../components/ConfirmDialog'
import { Panel } from '../../components/Panel'
import { useToast } from '../../components/Toast'
import { ExpiryText } from './AccessTokensCard'
import styles from './myAccess.module.css'

/** When a connected app was last used, or that it never was. */
export function GrantLastUsed({ grant }: { grant: GrantDTO }) {
  if (!grant.last_used_at) return <span>Never used</span>
  return (
    <span>
      Last used <Ago ts={grant.last_used_at} />
    </span>
  )
}

/** The signed-in user's connected apps (OAuth grants), with revoke. Renders nothing while OAuth or tokens are off. */
export function ConnectedAppsCard() {
  const tokens = useTokens()
  const revoke = useRevokeGrant()
  const toast = useToast()
  const [revoking, setRevoking] = useState<GrantDTO | null>(null)

  // Errors are shown by the tokens card, which reads the same query.
  if (!tokens.isSuccess || !tokens.data.policy.oauth) return null
  const grants = tokens.data.grants ?? []

  const confirmRevoke = () => {
    if (!revoking) return
    const target = revoking
    setRevoking(null)
    revoke.mutate(target.id, {
      onSuccess: () => toast.show(`Disconnected ${target.client_name}`, 'ok'),
      onError: (error) => toast.show(error.message, 'bad'),
    })
  }

  return (
    <>
      <Panel title="Connected apps" flush>
        {grants.length === 0 ? (
          <div className={styles.empty}>No connected apps. Approving a connector, such as claude.ai, adds it here.</div>
        ) : (
          <div className={styles.tokenList}>
            {grants.map((grant) => (
              <div key={grant.id} className={styles.tokenRow}>
                <div className={styles.tokenMain}>
                  <span className={styles.tokenName}>{grant.client_name}</span>
                  <span className={`${styles.tokenMeta} ${styles.mono}`}>{grant.client_id}</span>
                  <span className={styles.tokenMeta}>
                    Connected {new Date(grant.created_at).toLocaleDateString()} · <ExpiryText expiresAt={grant.expires_at} />
                  </span>
                  <span className={styles.tokenMeta}>
                    <GrantLastUsed grant={grant} />
                  </span>
                </div>
                <button type="button" className={`${styles.btn} ${styles.sm} ${styles.danger}`} aria-label={`Revoke ${grant.client_name}`} onClick={() => setRevoking(grant)}>
                  Revoke
                </button>
              </div>
            ))}
          </div>
        )}
      </Panel>
      {revoking && (
        <ConfirmDialog
          danger
          title={`Disconnect ${revoking.client_name}?`}
          sub="It stops working at once. To use it again, connect and approve it again."
          confirmLabel="Disconnect"
          onConfirm={confirmRevoke}
          onCancel={() => setRevoking(null)}
        />
      )}
    </>
  )
}
