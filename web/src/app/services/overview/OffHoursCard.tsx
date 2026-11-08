import { ApiError } from '../../../api/client'
import { useServiceOffHours, useSleepService, useWakeService } from '../../../api/offhours'
import type { ServiceDTO } from '../../../api/types'
import { Panel } from '../../../components/Panel'
import { Pill } from '../../../components/Pill'
import { useNow } from '../../../components/Ago'
import { useToast } from '../../../components/Toast'
import { stateKind, stateLabel, wallClock } from '../../settings/offhours/offhours'
import styles from './OffHoursCard.module.css'

const errorMessage = (error: unknown) => (error instanceof ApiError ? error.message : 'Something went wrong. Try again.')

/** Overview card for a service on an off-hours schedule: its state, and a wake or sleep action when Operate allows it. */
export function OffHoursCard({ service }: { service: ServiceDTO }) {
  const toast = useToast()
  const now = useNow(60_000)
  const { data } = useServiceOffHours(service.id)
  const wake = useWakeService(service.id)
  const sleep = useSleepService(service.id)

  if (!data || !data.enrolled || data.state === '') return null

  const state = data.state
  const asleep = state === 'asleep'
  const woken = state === 'woken'
  const wakeable = asleep || woken

  return (
    <Panel title="Off-hours">
      <div className={styles.body}>
        <p className={styles.line}>
          On &quot;{data.schedule_name}&quot; ({data.timezone}). <Pill kind={stateKind(state)}>{stateLabel({ state, until: data.until }, new Date(now), data.timezone)}</Pill>
        </p>
        {data.reason && <p className={styles.reason}>{data.reason}</p>}
        {wakeable && !data.can_operate && (
          <p className={styles.note}>{`You need Operate on ${service.tag.name} to ${asleep ? 'wake it' : 'put it back to sleep'}.`}</p>
        )}
        {wakeable && data.can_operate && (
          <div className={styles.actions}>
            {asleep && (
              <button
                type="button"
                className={styles.btn}
                disabled={wake.isPending}
                onClick={() =>
                  wake.mutate(undefined, {
                    onSuccess: (response) => toast.show(`Woke ${service.name} until ${wallClock(response.woken_until, data.timezone).time}.`, 'ok'),
                    onError: (error) => toast.show(errorMessage(error), 'bad'),
                  })
                }
              >
                Wake for 2 hours
              </button>
            )}
            {woken && (
              <button
                type="button"
                className={styles.btn}
                disabled={sleep.isPending}
                onClick={() =>
                  sleep.mutate(undefined, {
                    onSuccess: () => toast.show(`${service.name} is back to sleep.`, 'ok'),
                    onError: (error) => toast.show(errorMessage(error), 'bad'),
                  })
                }
              >
                Put back to sleep
              </button>
            )}
          </div>
        )}
      </div>
    </Panel>
  )
}
