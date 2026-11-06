import { useState } from 'react'
import { Link } from 'react-router'
import { useSettings } from '../../api/hooks'
import { usePhone } from '../../components/usePhone'
import { useAuth } from '../auth/AuthProvider'
import styles from './HiddenStoppedNote.module.css'

/** Says how old stopped tasks must be to stay hidden; admins get a link to change the window. On a phone the note can be dismissed. */
export function HiddenStoppedNote() {
  const settings = useSettings()
  const { me } = useAuth()
  const phone = usePhone()
  const [dismissed, setDismissed] = useState(false)
  const minutes = settings.data?.stopped_tasks_window_minutes
  if (minutes === undefined || (phone && dismissed)) return null

  return (
    <div className={styles.note}>
      <span>
        <span>{`Stopped tasks older than ${minutes} min are hidden.`}</span>
        {me?.admin && (
          <>
            {' '}
            <Link to="/settings/general" data-hm-rl="">
              Change
            </Link>
          </>
        )}
      </span>
      {phone && (
        <button type="button" className={styles.dismiss} onClick={() => setDismissed(true)}>
          Dismiss
        </button>
      )}
    </div>
  )
}
