import { useState } from 'react'
import { useSettings, useUpdateSettings } from '../../api/hooks'
import { Card } from '../../components/Card'
import { useToast } from '../../components/Toast'
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

/** Admin setting for how long stopped tasks stay visible on the Tasks page and service Tasks tab. */
export function StoppedTasksCard() {
  const settings = useSettings()
  const update = useUpdateSettings()
  const toast = useToast()
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

  return (
    <Card title="Stopped tasks">
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
        <button type="button" className={styles.saveButton} disabled={loadedMinutes === undefined || update.isPending} onClick={save}>
          Save
        </button>
      </div>
      {error && (
        <p role="alert" className={styles.error}>
          {error}
        </p>
      )}
    </Card>
  )
}
