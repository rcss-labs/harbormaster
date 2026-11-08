import { useState } from 'react'
import { createPortal } from 'react-dom'
import { useSettings, useUpdateSettings } from '../../api/hooks'
import { Panel } from '../../components/Panel'
import { useToast } from '../../components/Toast'
import { usePhone } from '../../components/usePhone'
import { useStickyBar } from './useStickyBar'
import { HeaderAction } from './HeaderAction'
import styles from './settings.module.css'

const MIN_MINUTES = 1
const MAX_MINUTES = 1440
const RANGE_MESSAGE = `Enter a number from ${MIN_MINUTES} to ${MAX_MINUTES}.`
const WINDOW_LABEL = 'Show stopped tasks from the last X minutes'

/** The whole-number window in range, or null so an invalid draft never reaches the API. */
function parseWindow(text: string): number | null {
  if (!/^\d+$/.test(text.trim())) return null
  const minutes = Number(text)
  return minutes >= MIN_MINUTES && minutes <= MAX_MINUTES ? minutes : null
}

/** "90" -> "1 h 30 min"; whole hours drop the minutes. */
function spellOut(minutes: number): string {
  if (minutes < 60) return `${minutes} min`
  const hours = Math.floor(minutes / 60)
  return minutes % 60 === 0 ? `${hours} h` : `${hours} h ${minutes % 60} min`
}

/** Admin setting for how long stopped tasks stay visible on the Tasks page and service Tasks tab. */
export function StoppedTasksCard() {
  const settings = useSettings()
  const update = useUpdateSettings()
  const toast = useToast()
  const phone = usePhone()
  useStickyBar(phone)
  const loadedMinutes = settings.data?.stopped_tasks_window_minutes
  // Null until the admin edits; until then the input shows the loaded server value.
  const [edited, setEdited] = useState<string | null>(null)
  const draft = edited ?? (loadedMinutes === undefined ? '' : String(loadedMinutes))
  const [error, setError] = useState('')

  const save = () => {
    const minutes = parseWindow(draft)
    if (minutes === null) {
      setError(RANGE_MESSAGE)
      return
    }
    setError('')
    update.mutate(minutes, {
      onSuccess: () => {
        // Drop the local edit so the input follows the server value refetched after the save.
        setEdited(null)
        toast.show('Saved.', 'ok')
      },
      onError: (caught) => toast.show(caught.message, 'bad'),
    })
  }

  const saveButton = (
    <button type="button" className={`${styles.btn} ${styles.primary}`} disabled={loadedMinutes === undefined || update.isPending} onClick={save}>
      Save
    </button>
  )

  return (
    <Panel title="Stopped tasks">
      {phone ? (
        createPortal(
          <div className={`hm-bar ${styles.saveBar}`} data-hm-bar="">
            {edited !== null ? <span className={styles.barNote}>Unsaved change</span> : <span className={styles.barNote}>Saved value: {loadedMinutes ?? '–'} minutes</span>}
            {saveButton}
          </div>,
          document.body,
        )
      ) : (
        <HeaderAction>{saveButton}</HeaderAction>
      )}
      <div className={styles.windowRow}>
        <span>Show stopped tasks from the last</span>
        <input
          type="number"
          min={MIN_MINUTES}
          max={MAX_MINUTES}
          aria-label={WINDOW_LABEL}
          className={styles.windowInput}
          value={draft}
          disabled={loadedMinutes === undefined}
          onChange={(event) => {
            setEdited(event.target.value)
            setError('')
          }}
        />
        <span>minutes</span>
        {parseWindow(draft) !== null && <span className={styles.hint}>{spellOut(Number(draft))}</span>}
      </div>
      {error && (
        <p role="alert" className={styles.error}>
          {error}
        </p>
      )}
    </Panel>
  )
}
