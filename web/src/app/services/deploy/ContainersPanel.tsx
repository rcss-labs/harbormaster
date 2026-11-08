import { useState } from 'react'
import { Chips } from '../../../components/Chips'
import { LockNote } from '../../../components/LockNote'
import { Panel } from '../../../components/Panel'
import { Seg } from '../../../components/Seg'
import { usePhone } from '../../../components/usePhone'
import { containersOf, imageTagOf, mainContainerIndex, type Definition } from '../task-definition/mainContainer'
import { CommandEditor } from './CommandEditor'
import { EnvEditor } from './EnvEditor'
import { HealthCheckEditor } from './HealthCheckEditor'
import { ImageTagPicker } from './ImageTagPicker'
import { SecretsEditor } from './SecretsEditor'
import {
  containerNames,
  readArgv,
  readEnvironment,
  readHealthCheck,
  readSecrets,
  setCommand,
  setContainerImageTag,
  setEntryPoint,
  setEnvironment,
  setHealthCheck,
  setSecrets,
} from './draft'
import styles from './containers.module.css'
import form from './editors.module.css'

interface ContainersPanelProps {
  serviceId: string
  /** The draft; every edit hands back a new definition through `onChange`. */
  definition: Definition
  /** The deployed definition, for the "deployed" mark in the tag list. */
  deployed: Definition
  onChange: (definition: Definition) => void
  canEdit: boolean
  isAdmin: boolean
  /** Name of the service's tag, for the "You need Edit on ..." lock title. */
  tagName: string
}

/** Repository part of an image reference (everything before the tag). */
const imageRepository = (image: unknown): string => {
  const text = typeof image === 'string' ? image : ''
  const tag = imageTagOf(text)
  return tag === '' ? text : text.slice(0, text.length - tag.length - 1)
}

/** Image tag, environment, secrets, command, entry point and health check of one container at a time. */
export function ContainersPanel({ serviceId, definition, deployed, onChange, canEdit, isAdmin, tagName }: ContainersPanelProps) {
  const phone = usePhone()
  const names = containerNames(definition)
  const [selected, setSelected] = useState<string | null>(null)
  const defaultName = names[Math.max(0, mainContainerIndex(definition, ''))] ?? ''
  const active = selected !== null && names.includes(selected) ? selected : defaultName
  const lock = canEdit ? undefined : `You need Edit on ${tagName}`

  const container = containersOf(definition).find((entry) => entry?.name === active)
  const deployedContainer = containersOf(deployed).find((entry) => entry?.name === active)
  if (!container) {
    return (
      <Panel title="Containers">
        <p className={form.empty}>This task definition has no containers.</p>
      </Panel>
    )
  }

  const switcherOptions = names.map((name) => ({ value: name, label: name }))
  return (
    <Panel
      title="Containers"
      sub={<span className={styles.mono}> · {imageRepository(container.image)}</span>}
      toolbar={names.length > 1 && !phone ? <Seg label="Container" options={switcherOptions} value={active} onChange={setSelected} /> : undefined}
    >
      {/* Phones: the switcher gets its own row (wrapping, so every container is reachable) and the lock is stated once. */}
      <div className={form.stack}>
        <LockNote>{lock}</LockNote>
        {phone && names.length > 1 && <Chips label="Container" options={switcherOptions} value={active} onChange={setSelected} wrap />}
        <fieldset className={styles.lockable} disabled={!canEdit} title={lock} key={active}>
          <div className={form.stack}>
            <div className={form.group}>
              <h3 className={form.groupTitle}>Image tag</h3>
              <ImageTagPicker
                serviceId={serviceId}
                container={active}
                currentTag={imageTagOf(container.image)}
                deployedTag={imageTagOf(deployedContainer?.image)}
                disabled={!canEdit}
                onChange={(tag) => onChange(setContainerImageTag(definition, active, tag))}
              />
            </div>
            <div className={form.group}>
              <h3 className={form.groupTitle}>Environment</h3>
              <EnvEditor entries={readEnvironment(definition, active)} onChange={(entries) => onChange(setEnvironment(definition, active, entries))} />
            </div>
            <div className={form.group}>
              <h3 className={form.groupTitle}>Secrets</h3>
              <SecretsEditor entries={readSecrets(definition, active)} isAdmin={isAdmin} onChange={(entries) => onChange(setSecrets(definition, active, entries))} />
            </div>
            <div className={form.group}>
              <h3 className={form.groupTitle}>Command</h3>
              <CommandEditor label="Command" argv={readArgv(definition, active, 'command')} onChange={(argv) => onChange(setCommand(definition, active, argv))} />
            </div>
            <div className={form.group}>
              <h3 className={form.groupTitle}>Entry point</h3>
              <CommandEditor label="Entry point" argv={readArgv(definition, active, 'entryPoint')} onChange={(argv) => onChange(setEntryPoint(definition, active, argv))} />
            </div>
            <div className={form.group}>
              <h3 className={form.groupTitle}>Health check</h3>
              <HealthCheckEditor check={readHealthCheck(definition, active)} onChange={(check) => onChange(setHealthCheck(definition, active, check))} />
            </div>
          </div>
        </fieldset>
      </div>
    </Panel>
  )
}
