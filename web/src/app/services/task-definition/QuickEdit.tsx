import { Banner } from '../../../components/Banner'
import type { ImageTagsResponse } from '../../../api/change'
import {
  FARGATE_CPU_VALUES,
  IMAGE_TAG_RE,
  containersOf,
  fargateMemoryOptions,
  imageTagOf,
  mainContainerIndex,
  updateMainContainer,
  withImageTag,
  type Definition,
} from './mainContainer'
import styles from './taskDefinition.module.css'

interface QuickEditProps {
  definition: Definition
  mainContainer: string
  imageTags: ImageTagsResponse | undefined
  deployedTag: string
  oomHint?: { memory: string; suggested: string }
  disabled: boolean
  onChange(next: Definition): void
}

/** Only free-text tags are checked, and only once the user has changed them. */
export function isTagInvalid(source: string | undefined, tag: string, deployedTag: string): boolean {
  return source === 'none' && tag !== deployedTag && !IMAGE_TAG_RE.test(tag)
}

interface EnvEntry {
  name: string
  value: string
}

function readString(value: unknown): string {
  return typeof value === 'number' ? String(value) : typeof value === 'string' ? value : ''
}

/** Form over the draft JSON: every control rewrites the document and hands it back. */
export function QuickEdit({ definition, mainContainer, imageTags, deployedTag, oomHint, disabled, onChange }: QuickEditProps) {
  const containers = containersOf(definition)
  const index = containers.findIndex((container) => container?.name === mainContainer)
  const main = containers[index >= 0 ? index : mainContainerIndex(definition, mainContainer)]
  const mainName = readString(main?.name) || mainContainer
  const currentTag = imageTagOf(main?.image)
  const cpu = readString(definition.cpu)
  const memory = readString(definition.memory)
  const environment = (Array.isArray(main?.environment) ? main.environment : []) as EnvEntry[]

  const setMain = (change: (container: Record<string, unknown>) => Record<string, unknown>) =>
    onChange(updateMainContainer(definition, mainName, change))
  const setTag = (tag: string) => setMain((container) => ({ ...container, image: withImageTag(container.image, tag) }))
  const setEnvironment = (next: EnvEntry[]) => setMain((container) => ({ ...container, environment: next }))

  // The memory stays as it is, even when invalid for the new CPU; the select keeps it as an extra option.
  const setCpu = (nextCpu: string) => onChange({ ...definition, cpu: nextCpu })

  const cpuOptions = FARGATE_CPU_VALUES.includes(cpu) || cpu === '' ? FARGATE_CPU_VALUES : [...FARGATE_CPU_VALUES, cpu]
  const memoryValid = fargateMemoryOptions(cpu).map(String)
  const memoryOptions = memory === '' || memoryValid.includes(memory) ? memoryValid : [memory, ...memoryValid]

  const ecrTags = imageTags?.source === 'ecr' ? imageTags.tags.map((entry) => entry.tag) : []
  const tagChoices = currentTag && !ecrTags.includes(currentTag) ? [currentTag, ...ecrTags] : ecrTags
  const tagLabel = (tag: string) => {
    const marks = [tag === ecrTags[0] && tag !== deployedTag ? 'newest' : '', tag === deployedTag ? 'deployed' : ''].filter(Boolean)
    return [tag, ...marks].join(' · ')
  }
  const tagInvalid = isTagInvalid(imageTags?.source, currentTag, deployedTag)

  return (
    <fieldset className={styles.fieldset} disabled={disabled}>
      {imageTags?.source === 'ecr' ? (
        <label className={styles.field}>
          <span>{`Image tag (from ECR: ${imageTags.repository})`}</span>
          <select className={styles.ctl} aria-label="Image tag" value={currentTag} onChange={(event) => setTag(event.target.value)}>
            {tagChoices.map((tag) => (
              <option key={tag} value={tag}>
                {tagLabel(tag)}
              </option>
            ))}
          </select>
        </label>
      ) : (
        <label className={styles.field}>
          <span>Image tag</span>
          <input className={`${styles.ctl} ${styles.mono}`} aria-label="Image tag" value={currentTag} onChange={(event) => setTag(event.target.value)} />
          {tagInvalid && <span className={styles.fieldError}>Tag may contain letters, digits, . _ -</span>}
        </label>
      )}
      <div className={styles.pair}>
        <label className={styles.field}>
          <span>Task CPU (units)</span>
          <select className={styles.ctl} aria-label="Task CPU" value={cpu} onChange={(event) => setCpu(event.target.value)}>
            {cpuOptions.map((value) => (
              <option key={value}>{value}</option>
            ))}
          </select>
        </label>
        <label className={styles.field}>
          <span>Task memory (MiB)</span>
          <select className={styles.ctl} aria-label="Task memory" value={memory} onChange={(event) => onChange({ ...definition, memory: event.target.value })}>
            {memoryOptions.map((value) => (
              <option key={value}>{value}</option>
            ))}
          </select>
        </label>
      </div>
      <div className={styles.field}>
        <span>{`Environment variables (${mainName})`}</span>
        {environment.map((entry, position) => (
          <div className={styles.envRow} key={position}>
            <input
              className={`${styles.ctl} ${styles.mono}`}
              aria-label="Name"
              value={readString(entry.name)}
              onChange={(event) => setEnvironment(environment.map((row, at) => (at === position ? { ...row, name: event.target.value } : row)))}
            />
            <input
              className={`${styles.ctl} ${styles.mono}`}
              aria-label="Value"
              value={readString(entry.value)}
              onChange={(event) => setEnvironment(environment.map((row, at) => (at === position ? { ...row, value: event.target.value } : row)))}
            />
            <button type="button" className={styles.iconBtn} aria-label="Remove variable" onClick={() => setEnvironment(environment.filter((_, at) => at !== position))}>
              ×
            </button>
          </div>
        ))}
        <div>
          <button type="button" className={styles.btn} onClick={() => setEnvironment([...environment, { name: 'NEW_VAR', value: '' }])}>
            Add variable
          </button>
        </div>
      </div>
      {oomHint && (
        <Banner kind="warn">{`Tasks of this service stop with OutOfMemoryError (exit 137) at ${oomHint.memory} MiB. Try ${oomHint.suggested} MiB.`}</Banner>
      )}
    </fieldset>
  )
}
