import { useId, type FormEvent, ReactNode } from 'react'
import { Brand } from '../../components/Brand'
import { usePhone } from '../../components/useNarrow'
import styles from './auth.module.css'

interface AuthCardProps {
  title: string
  sub?: string
  error?: string
  /** Omit to render the card without a submit button (SSO-only view, loading frame). */
  submitLabel?: string
  info?: string
  busy: boolean
  onSubmit: () => void
  children: ReactNode
}

export function AuthCard({ title, sub, error, info, submitLabel, busy, onSubmit, children }: AuthCardProps) {
  const handleSubmit = (event: FormEvent) => {
    event.preventDefault()
    onSubmit()
  }
  return (
    <main className={styles.wrap}>
      <form className={`card ${styles.card}`} onSubmit={handleSubmit} noValidate>
        <div className={styles.brand}>
          <Brand variant="signin" />
          <div className={styles.brandSub}>ECS control center</div>
        </div>
        <h1 className={styles.title}>{title}</h1>
        {sub && <p className={styles.sub}>{sub}</p>}
        {info && <p className={styles.sub}>{info}</p>}
        {children}
        {error && (
          <div className={styles.err} role="alert">
            {error}
          </div>
        )}
        {submitLabel && (
          <button className={`btn primary ${styles.submit}`} type="submit" disabled={busy}>
            {submitLabel}
          </button>
        )}
      </form>
    </main>
  )
}

interface FieldProps {
  label: string
  value: string
  onChange: (value: string) => void
  type?: string
  autoComplete?: string
  hint?: string
}

export function Field({ label, value, onChange, type = 'text', autoComplete, hint }: FieldProps) {
  const id = useId()
  const phone = usePhone()
  // A phone keyboard must not capitalise or "correct" usernames, tokens and passwords.
  const keyboard = phone ? { autoCapitalize: 'none', autoCorrect: 'off', spellCheck: false } : {}
  return (
    <div className={styles.field}>
      <label htmlFor={id}>{label}</label>
      <input id={id} className={styles.ctl} type={type} value={value} autoComplete={autoComplete} onChange={(e) => onChange(e.target.value)} required {...keyboard} />
      {hint && <span className={styles.hint}>{hint}</span>}
    </div>
  )
}
