import { Banner } from '../../../components/Banner'
import { Panel } from '../../../components/Panel'
import { FARGATE_CPU_VALUES, fargateMemoryOptions, type Definition, type OomHint } from '../task-definition/mainContainer'
import { setTaskSize } from './draft'
import styles from './containers.module.css'
import form from './editors.module.css'

interface TaskSizePanelProps {
  definition: Definition
  onChange: (definition: Definition) => void
  canEdit: boolean
  /** Name of the service's tag, for the "You need Edit on ..." lock title. */
  tagName: string
  oomHint?: OomHint
}

const asText = (value: unknown): string => (typeof value === 'number' ? String(value) : typeof value === 'string' ? value : '')

/** Task-level CPU and memory. Memory choices follow the CPU, as Fargate only allows certain pairs. */
export function TaskSizePanel({ definition, onChange, canEdit, tagName, oomHint }: TaskSizePanelProps) {
  const cpu = asText(definition.cpu)
  const memory = asText(definition.memory)
  const lock = canEdit ? undefined : `You need Edit on ${tagName}`

  // A value outside the Fargate table (an EC2 task, or memory left over from another CPU) stays visible as an extra option.
  const cpuOptions = cpu === '' || FARGATE_CPU_VALUES.includes(cpu) ? FARGATE_CPU_VALUES : [...FARGATE_CPU_VALUES, cpu]
  const validMemory = fargateMemoryOptions(cpu).map(String)
  const memoryOptions = memory === '' || validMemory.includes(memory) ? validMemory : [memory, ...validMemory]
  const memoryMismatch = memory !== '' && validMemory.length > 0 && !validMemory.includes(memory)

  return (
    <Panel title="Task size" sub={<span className={styles.mono}> · CPU units, memory MiB</span>}>
      <fieldset className={styles.lockable} disabled={!canEdit} title={lock}>
        <div className={form.stack}>
          <div className={form.fieldRow}>
            <label className={form.field}>
              Task CPU (units)
              <select className={form.select} aria-label="Task CPU" value={cpu} onChange={(event) => onChange(setTaskSize(definition, { cpu: event.target.value }))}>
                {cpuOptions.map((value) => (
                  <option key={value}>{value}</option>
                ))}
              </select>
            </label>
            <label className={form.field}>
              Task memory (MiB)
              <select className={form.select} aria-label="Task memory" value={memory} onChange={(event) => onChange(setTaskSize(definition, { memory: event.target.value }))}>
                {memoryOptions.map((value) => (
                  <option key={value}>{value}</option>
                ))}
              </select>
            </label>
          </div>
          {memoryMismatch && <p className={form.warn}>{`${memory} MiB is not available with ${cpu} CPU units. Pick one of the listed sizes.`}</p>}
          {oomHint && <Banner kind="warn">{`Tasks of this service stop with OutOfMemoryError (exit 137) at ${oomHint.memory} MiB. Try ${oomHint.suggested} MiB.`}</Banner>}
        </div>
      </fieldset>
    </Panel>
  )
}
