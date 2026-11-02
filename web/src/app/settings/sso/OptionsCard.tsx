import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { api } from '../../../api/client'
import type { SSOConfigDTO } from '../../../api/types'
import { Panel } from '../../../components/Panel'
import { useToast } from '../../../components/Toast'
import { configBody } from './configBody'
import styles from './sso.module.css'

const MAX_SKEW_SECONDS = 300

interface Draft {
  button_label: string
  name_id_format: string
  attr_email: string
  attr_display_name: string
  attr_username: string
  clock_skew_seconds: string
}

const draftOf = (config: SSOConfigDTO): Draft => ({
  button_label: config.button_label,
  name_id_format: config.name_id_format,
  attr_email: config.attr_email ?? '',
  attr_display_name: config.attr_display_name ?? '',
  attr_username: config.attr_username ?? '',
  clock_skew_seconds: String(config.clock_skew_seconds),
})

export function OptionsCard({ config }: { config: SSOConfigDTO }) {
  const queryClient = useQueryClient()
  const toast = useToast()
  // Null until the admin edits; until then the form follows the saved configuration.
  const [edited, setEdited] = useState<Draft | null>(null)
  const [error, setError] = useState('')
  const draft = edited ?? draftOf(config)
  const set = (field: keyof Draft, value: string) => {
    setEdited({ ...draft, [field]: value })
    setError('')
  }

  const save = useMutation({
    mutationFn: (body: Record<string, unknown>) => api.put<SSOConfigDTO>('/api/sso', body),
    onSuccess: (updated) => {
      queryClient.setQueryData(['sso'], updated)
      setEdited(null)
      toast.show('Saved.', 'ok')
    },
    onError: (caught) => setError(caught.message),
  })

  const submit = () => {
    const skew = draft.clock_skew_seconds.trim()
    const seconds = Number(skew)
    if (!/^\d+$/.test(skew) || seconds > MAX_SKEW_SECONDS) {
      setError(`Enter a clock skew from 0 to ${MAX_SKEW_SECONDS} seconds.`)
      return
    }
    const optional = (value: string) => (value.trim() === '' ? null : value.trim())
    save.mutate(
      configBody(config, {
        button_label: draft.button_label.trim(),
        name_id_format: draft.name_id_format,
        attr_email: optional(draft.attr_email),
        attr_display_name: optional(draft.attr_display_name),
        attr_username: optional(draft.attr_username),
        clock_skew_seconds: seconds,
      }),
    )
  }

  return (
    <Panel title="Sign-in options">
      <div className={`${styles.form} ${styles.formWide}`}>
        <div className={styles.grid}>
          <label className={styles.field}>
            <span>Button label</span>
            <input className={styles.ctl} value={draft.button_label} onChange={(event) => set('button_label', event.target.value)} />
          </label>
          <label className={styles.field}>
            <span>NameID format</span>
            <select className={styles.ctl} value={draft.name_id_format} onChange={(event) => set('name_id_format', event.target.value)}>
              <option value="unspecified">Let the IdP choose</option>
              <option value="email">Email address</option>
              <option value="persistent">Persistent</option>
            </select>
          </label>
          <label className={styles.field}>
            <span>Email attribute</span>
            <input
              className={styles.ctl}
              placeholder="email"
              value={draft.attr_email}
              onChange={(event) => set('attr_email', event.target.value)}
            />
          </label>
          <label className={styles.field}>
            <span>Display name attribute</span>
            <input
              className={styles.ctl}
              placeholder="displayName"
              value={draft.attr_display_name}
              onChange={(event) => set('attr_display_name', event.target.value)}
            />
          </label>
          <label className={styles.field}>
            <span>Username attribute</span>
            <input
              className={styles.ctl}
              placeholder="Derived from email"
              value={draft.attr_username}
              onChange={(event) => set('attr_username', event.target.value)}
            />
          </label>
          <label className={styles.field}>
            <span>Clock skew (seconds)</span>
            <input
              className={styles.ctl}
              type="number"
              min={0}
              max={MAX_SKEW_SECONDS}
              value={draft.clock_skew_seconds}
              onChange={(event) => set('clock_skew_seconds', event.target.value)}
            />
          </label>
        </div>
        {error && (
          <p role="alert" className={styles.err}>
            {error}
          </p>
        )}
        <div className={styles.row}>
          <button type="button" className={`${styles.btn} ${styles.primary}`} disabled={save.isPending} onClick={submit}>
            Save
          </button>
        </div>
      </div>
    </Panel>
  )
}
