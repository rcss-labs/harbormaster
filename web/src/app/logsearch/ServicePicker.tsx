import { useId, useMemo, useState, type KeyboardEvent } from 'react'
import type { LogSearchServiceDTO } from '../../api/logSearch'
import { MAX_SERVICES } from './model'
import styles from './logsearch.module.css'

interface ServicePickerProps {
  services: LogSearchServiceDTO[]
  selectedIds: string[]
  onChange: (ids: string[]) => void
}

const matches = (service: LogSearchServiceDTO, needle: string) =>
  [service.name, service.cluster, service.account_name, service.account_id].some((field) => field.toLowerCase().includes(needle))

/** Groups services by account (in the order the API lists them), keeping a flat list for keyboard navigation. */
function groupByAccount(services: LogSearchServiceDTO[]) {
  const groups = new Map<string, { label: string; services: LogSearchServiceDTO[] }>()
  for (const service of services) {
    const group = groups.get(service.account_id) ?? { label: service.account_name || service.account_id, services: [] }
    group.services.push(service)
    groups.set(service.account_id, group)
  }
  return [...groups.values()]
}

/** Selected services as removable chips, plus a combobox that adds one more from the services the API allows. */
export function ServicePicker({ services, selectedIds, onChange }: ServicePickerProps) {
  const listId = useId()
  const [text, setText] = useState('')
  const [open, setOpen] = useState(false)
  const [active, setActive] = useState(0)
  const full = selectedIds.length >= MAX_SERVICES
  const byId = useMemo(() => new Map(services.map((service) => [service.id, service])), [services])

  const needle = text.trim().toLowerCase()
  const candidates = services.filter((service) => !selectedIds.includes(service.id) && (needle === '' || matches(service, needle)))
  const groups = groupByAccount(candidates)
  const flat = groups.flatMap((group) => group.services)
  const activeIndex = Math.min(active, Math.max(flat.length - 1, 0))

  const add = (service: LogSearchServiceDTO) => {
    onChange([...selectedIds, service.id])
    setText('')
    setActive(0)
    if (selectedIds.length + 1 >= MAX_SERVICES) setOpen(false)
  }

  const onKeyDown = (event: KeyboardEvent<HTMLInputElement>) => {
    if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
      event.preventDefault()
      setOpen(true)
      const step = event.key === 'ArrowDown' ? 1 : -1
      setActive((flat.length + activeIndex + step) % Math.max(flat.length, 1))
    } else if (event.key === 'Enter' && open && flat[activeIndex]) {
      event.preventDefault()
      add(flat[activeIndex])
    } else if (event.key === 'Escape' && open) {
      event.stopPropagation()
      setOpen(false)
    }
  }

  return (
    <div className={styles.field}>
      <span className={styles.fieldLabel}>Services</span>
      <ul className={styles.chips} aria-label="Selected services">
        {selectedIds.map((id) => {
          const service = byId.get(id)
          const name = service?.name ?? id
          return (
            <li key={id} className={`${styles.chip} ${service ? '' : styles.chipGone}`.trim()} title={service ? `${service.cluster} · ${service.account_name}` : 'Not available to you'}>
              <span>{name}</span>
              <button type="button" className={styles.chipRemove} aria-label={`Remove ${name}`} onClick={() => onChange(selectedIds.filter((other) => other !== id))}>
                ×
              </button>
            </li>
          )
        })}
      </ul>
      <div className={styles.picker}>
        <input
          className={styles.control}
          role="combobox"
          aria-label="Add service"
          aria-expanded={open}
          aria-controls={listId}
          aria-autocomplete="list"
          aria-activedescendant={open && flat[activeIndex] ? `${listId}-${flat[activeIndex].id}` : undefined}
          placeholder={full ? `Up to ${MAX_SERVICES} services` : 'Add service: search by name, cluster or account'}
          disabled={full}
          autoComplete="off"
          value={text}
          onChange={(event) => {
            setText(event.target.value)
            setActive(0)
            setOpen(true)
          }}
          onFocus={() => setOpen(true)}
          onClick={() => setOpen(true)}
          onBlur={() => setOpen(false)}
          onKeyDown={onKeyDown}
        />
        {open && !full && (
          // Mouse down on an option must not blur the input first, or the list closes before the click lands.
          <div id={listId} role="listbox" aria-label="Services you can search" className={styles.list} onMouseDown={(event) => event.preventDefault()}>
            {flat.length === 0 && <div className={styles.noMatch}>{services.length === candidates.length ? 'No services match.' : 'No more services match.'}</div>}
            {groups.map((group) => (
              <div key={group.label} role="group" aria-label={group.label} className={styles.group}>
                <div className={styles.groupName} aria-hidden="true">
                  {group.label}
                </div>
                {group.services.map((service) => (
                  <div
                    key={service.id}
                    id={`${listId}-${service.id}`}
                    role="option"
                    aria-selected={flat[activeIndex]?.id === service.id}
                    className={styles.option}
                    onClick={() => add(service)}
                  >
                    <span>{service.name}</span>
                    <span className={styles.optionSub}>{`${service.cluster} · ${service.region}`}</span>
                  </div>
                ))}
              </div>
            ))}
          </div>
        )}
      </div>
    </div>
  )
}
