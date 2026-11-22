import { useState } from 'react'
import type { TokenPolicyDTO } from '../../api/tokens'
import { useCreateToken } from '../../api/tokens'
import { Dialog } from '../../components/Dialog'
import { useToast } from '../../components/Toast'
import styles from './myAccess.module.css'

const NAME_MAX = 60
const LIFETIME_CHOICES = [1, 7, 30, 90]

/** Lifetime choices up to the policy maximum; the maximum itself is always offered. */
export function lifetimeChoices(maxDays: number): number[] {
  const choices = LIFETIME_CHOICES.filter((days) => days < maxDays)
  return [...choices, maxDays]
}

const dayLabel = (days: number) => (days === 1 ? '1 day' : `${days} days`)

/** Why the longest lifetime is short, for the users it applies to. */
function shortLifetimeReason(policy: TokenPolicyDTO): string | null {
  if (policy.sso_linked) return `Tokens of single sign-on users last at most ${dayLabel(policy.max_ttl_days)}, so access ends soon after it is removed at the identity provider.`
  if (policy.admin) return `Tokens of global admins last at most ${dayLabel(policy.max_ttl_days)}.`
  return null
}

/**
 * Creates a token, then shows its secret once. The secret lives only in this component's state: closing the
 * dialog unmounts it, and nothing is written to the query cache, storage or the console.
 */
export function CreateTokenDialog({ policy, onClose }: { policy: TokenPolicyDTO; onClose: () => void }) {
  const toast = useToast()
  const create = useCreateToken()
  const [name, setName] = useState('')
  const [days, setDays] = useState(policy.max_ttl_days)
  const [secret, setSecret] = useState<string | null>(null)

  const trimmed = name.trim()
  const reason = shortLifetimeReason(policy)

  const submit = () => {
    create.mutate(
      { name: trimmed, expires_in_days: days },
      { onSuccess: (result) => setSecret(result.secret) },
    )
  }
  const copy = async (text: string, what: string) => {
    try {
      await navigator.clipboard.writeText(text)
      toast.show(`${what} copied`, 'ok')
    } catch {
      toast.show(`Could not copy the ${what.toLowerCase()}.`, 'bad')
    }
  }

  if (secret !== null) {
    const command = policy.mcp_url
      ? `claude mcp add --transport http harbormaster ${policy.mcp_url.replace(/\/+$/, '')}/mcp --header "Authorization: Bearer ${secret}"`
      : null
    return (
      <Dialog title="Token created" sub="This is the only time the token is shown." actions={[{ label: 'Done', kind: 'primary', onClick: onClose }]} onClose={onClose}>
        <div className={styles.stack}>
          <div className={styles.secretRow}>
            <div className={styles.secret} data-testid="token-secret">
              {secret}
            </div>
            <button type="button" className={styles.btn} onClick={() => void copy(secret, 'Token')}>
              Copy
            </button>
          </div>
          <p className={styles.hint}>Copy it now. It cannot be shown again; if you lose it, revoke it and create a new one.</p>
          {command && (
            <>
              <div className={styles.secretRow}>
                <div className={`${styles.secret} ${styles.command}`}>{command}</div>
                <button type="button" className={styles.btn} onClick={() => void copy(command, 'Command')}>
                  Copy
                </button>
              </div>
              <p className={styles.hint}>Run this to connect Claude Code.</p>
            </>
          )}
        </div>
      </Dialog>
    )
  }

  return (
    <Dialog
      title="Create access token"
      sub="A token reads what you can see, never more. It cannot change anything."
      actions={[
        { label: 'Cancel', onClick: onClose },
        { label: 'Create token', kind: 'primary', disabled: trimmed === '' || trimmed.length > NAME_MAX || create.isPending, onClick: submit },
      ]}
      onClose={onClose}
    >
      <div className={styles.stack}>
        <label className={styles.field}>
          <span>Name</span>
          <input className={styles.ctl} autoComplete="off" maxLength={NAME_MAX} placeholder="Claude Code on my laptop" value={name} onChange={(event) => setName(event.target.value)} />
        </label>
        <label className={styles.field}>
          <span>Lifetime</span>
          <select className={styles.ctl} value={days} onChange={(event) => setDays(Number(event.target.value))}>
            {lifetimeChoices(policy.max_ttl_days).map((choice) => (
              <option key={choice} value={choice}>
                {dayLabel(choice)}
              </option>
            ))}
          </select>
        </label>
        {reason && <p className={styles.hint}>{reason}</p>}
        <div className={styles.err} role="alert">
          {create.error?.message}
        </div>
      </div>
    </Dialog>
  )
}
