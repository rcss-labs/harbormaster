import { VALUE_FROM_HINT, isValidValueFrom, type SecretEntry } from './draft'
import styles from './containers.module.css'
import form from './editors.module.css'

interface SecretsEditorProps {
  entries: SecretEntry[]
  onChange: (entries: SecretEntry[]) => void
  /** Global admins may reference any secret; everyone else is limited by the server to prefixes the task already uses. */
  isAdmin: boolean
}

/**
 * Secrets are references: a name for the container and where ECS reads the value from at task start.
 * The value itself is never fetched or shown here.
 */
export function SecretsEditor({ entries, onChange, isAdmin }: SecretsEditorProps) {
  const update = (position: number, patch: Partial<SecretEntry>) => onChange(entries.map((entry, at) => (at === position ? { ...entry, ...patch } : entry)))
  return (
    <div className={styles.rows}>
      <p className={form.hint}>
        ECS reads these when a task starts, using the task execution role. The role needs secretsmanager:GetSecretValue, ssm:GetParameters and kms:Decrypt (for encrypted secrets) on each reference; Harbormaster does not check this.
      </p>
      {!isAdmin && (
        <p className={form.hint}>
          Without global admin you can add only secrets from this AWS account under a prefix this task definition already uses (for example the prod/ in arn:aws:secretsmanager:region:account:secret:prod/db). Other changes need a global admin.
        </p>
      )}
      {entries.length === 0 && <p className={form.hint}>No secrets.</p>}
      {entries.map((entry, position) => {
        const invalid = !isValidValueFrom(entry.valueFrom)
        return (
          <div className={styles.secretRow} key={position}>
            <input className={`${form.input} ${form.mono}`} aria-label="Secret name" spellCheck={false} value={entry.name} onChange={(event) => update(position, { name: event.target.value })} />
            <input
              className={`${form.input} ${form.mono} ${invalid ? styles.invalid : ''}`.trim()}
              aria-label="Value from"
              aria-invalid={invalid || undefined}
              spellCheck={false}
              value={entry.valueFrom}
              onChange={(event) => update(position, { valueFrom: event.target.value })}
            />
            <button type="button" className={styles.iconBtn} aria-label={`Remove secret ${entry.name || position + 1}`} onClick={() => onChange(entries.filter((_, at) => at !== position))}>
              ×
            </button>
            {invalid && (
              <p className={styles.rowError} role="alert">
                {VALUE_FROM_HINT}
              </p>
            )}
          </div>
        )
      })}
      <div className={form.actions}>
        <button type="button" className={form.btn} onClick={() => onChange([...entries, { name: '', valueFrom: '' }])}>
          Add secret
        </button>
      </div>
    </div>
  )
}
