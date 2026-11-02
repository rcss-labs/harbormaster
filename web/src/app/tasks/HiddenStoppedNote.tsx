import { Link } from 'react-router'
import { useSettings } from '../../api/hooks'
import { useAuth } from '../auth/AuthProvider'
import styles from './HiddenStoppedNote.module.css'

/** Says how old stopped tasks must be to stay hidden; admins get a link to change the window. */
export function HiddenStoppedNote() {
  const settings = useSettings()
  const { me } = useAuth()
  const minutes = settings.data?.stopped_tasks_window_minutes
  if (minutes === undefined) return null

  return (
    <div className={styles.note}>
      <span>{`Stopped tasks older than ${minutes} min are hidden.`}</span>
      {me?.admin && (
        <>
          {' '}
          <Link to="/settings/general">Change</Link>
        </>
      )}
    </div>
  )
}
