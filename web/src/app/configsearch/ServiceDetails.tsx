import type { ConfigSearchItemDTO } from '../../api/configSearch'
import styles from './configsearch.module.css'

const orNone = (values: string[]) => (values.length > 0 ? values.join(' ') : '–')

/** Every container of the matched revision with its names, plus both roles. Names only, never values. */
export function ServiceDetails({ item }: { item: ConfigSearchItemDTO }) {
  return (
    <div className={styles.details}>
      {item.containers.map((container) => (
        <section key={container.name} className={styles.container} aria-label={`Container ${container.name}`}>
          <h3 className={styles.containerName}>{container.name}</h3>
          <dl className={styles.kv}>
            <dt>Image</dt>
            <dd>{container.image}</dd>
            <dt>Base image</dt>
            <dd>{container.base_image || '–'}</dd>
            <dt>Ports</dt>
            <dd>{container.ports.length > 0 ? container.ports.join(' ') : '–'}</dd>
            <dt>Environment</dt>
            <dd>{orNone(container.env_names)}</dd>
            <dt>Secrets</dt>
            <dd>{orNone(container.secret_names)}</dd>
            <dt>Log group</dt>
            <dd>{container.log_group || '–'}</dd>
          </dl>
        </section>
      ))}
      <dl className={styles.kv}>
        <dt>Task role</dt>
        <dd>{item.task_role || '–'}</dd>
        <dt>Execution role</dt>
        <dd>{item.execution_role || '–'}</dd>
      </dl>
    </div>
  )
}
