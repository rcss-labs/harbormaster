import { useState } from 'react'
import { useSaveAgentIdentity, type AgentIdentityDTO } from '../../../api/agent'
import { ApiError } from '../../../api/client'
import { useOffHoursTags } from '../../../api/offhours'
import { Panel } from '../../../components/Panel'
import { useToast } from '../../../components/Toast'
import styles from '../settings.module.css'

type Access = 'none' | 'view' | 'logs'

const ACCESS_LABELS: Record<Access, string> = { none: 'No access', view: 'View', logs: 'View and logs' }

interface Row {
  id: number
  name: string
}

const accessOf = (identity: AgentIdentityDTO, tagId: number): Access => {
  const entry = (identity.permissions ?? []).find((permission) => permission.tag_id === tagId)
  return entry ? (entry.logs ? 'logs' : 'view') : 'none'
}

/** What the agent identity may read in background investigations: per tag, no access, View, or View and logs. */
export function IdentitySection({ identity }: { identity: AgentIdentityDTO }) {
  const tags = useOffHoursTags()
  const save = useSaveAgentIdentity()
  const toast = useToast()
  const [edited, setEdited] = useState<Record<number, Access>>({})
  const [saveError, setSaveError] = useState<string | null>(null)

  // Every tag, plus any the identity still holds that the tag list no longer shows.
  const rows: Row[] = (tags.data?.harbormaster ?? []).map((tag) => ({ id: tag.id, name: tag.name }))
  for (const permission of identity.permissions ?? []) {
    if (!rows.some((row) => row.id === permission.tag_id)) rows.push({ id: permission.tag_id, name: permission.tag_name })
  }
  const current = (row: Row): Access => edited[row.id] ?? accessOf(identity, row.id)
  const dirty = rows.some((row) => current(row) !== accessOf(identity, row.id))

  const submit = () => {
    const permissions = rows.filter((row) => current(row) !== 'none').map((row) => ({ tag_id: row.id, logs: current(row) === 'logs' }))
    setSaveError(null)
    save.mutate(
      { permissions },
      {
        onSuccess: () => {
          setEdited({})
          toast.show('Saved.', 'ok')
        },
        onError: (error) => {
          const message = error instanceof ApiError ? error.message : 'Could not save the access. Try again.'
          setSaveError(message)
          toast.show(message, 'bad')
        },
      },
    )
  }

  return (
    <Panel
      title="Agent identity"
      sub={identity.username}
      loading={tags.isPending}
      error={tags.isError ? { message: 'Could not load the tags.', onRetry: () => void tags.refetch() } : undefined}
    >
      <p className={styles.hint}>Background investigations read only what this identity can see. It cannot sign in or change anything.</p>
      {rows.length === 0 ? (
        <p>There are no tags yet. Tag services to give the agent access to them.</p>
      ) : (
        <>
          {rows.map((row) => (
            <div key={row.id} className={styles.windowRow}>
              <label htmlFor={`agent-access-${row.id}`} style={{ minWidth: 160 }}>{`Access to ${row.name}`}</label>
              <select
                id={`agent-access-${row.id}`}
                className={styles.select}
                value={current(row)}
                onChange={(event) => setEdited((current) => ({ ...current, [row.id]: event.target.value as Access }))}
              >
                {(Object.keys(ACCESS_LABELS) as Access[]).map((access) => (
                  <option key={access} value={access}>
                    {ACCESS_LABELS[access]}
                  </option>
                ))}
              </select>
            </div>
          ))}
          <div className={styles.windowRow}>
            <button type="button" className={styles.btn} disabled={!dirty || save.isPending} onClick={submit}>
              Save access
            </button>
            {dirty && (
              <span className={styles.hint} data-access-cue="">
                Unsaved changes
              </span>
            )}
          </div>
          {saveError && (
            <p role="alert" className={styles.error}>
              {saveError}
            </p>
          )}
        </>
      )}
    </Panel>
  )
}
