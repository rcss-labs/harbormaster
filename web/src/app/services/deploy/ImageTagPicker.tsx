import { useEffect, useId, useState, type KeyboardEvent } from 'react'
import { useContainerImageTags } from '../../../api/deploy'
import { Ago } from '../../../components/Ago'
import { BottomSheet } from '../../../components/BottomSheet'
import { usePhone } from '../../../components/usePhone'
import { IMAGE_TAG_RE } from '../task-definition/mainContainer'
import styles from './containers.module.css'
import form from './editors.module.css'

export interface ImageTagPickerProps {
  serviceId: string
  container: string
  currentTag: string
  /** The tag of the deployed revision, marked in the list. */
  deployedTag: string
  disabled?: boolean
  onChange: (tag: string) => void
}

const SEARCH_DEBOUNCE_MS = 250
/** On a phone, ECR registries with more tags than this list and search them in a sheet instead of a dropdown. */
const SHEET_TAG_THRESHOLD = 8

/**
 * Image tag combobox. With ECR it lists the newest tags and searches all of them on the server as you type;
 * for other registries it is a plain text field that only accepts tags the server would accept.
 */
export function ImageTagPicker({ serviceId, container, currentTag, deployedTag, disabled = false, onChange }: ImageTagPickerProps) {
  const listboxId = useId()
  // Null shows the current tag; text is what the user typed (a search in ECR mode, the new tag otherwise).
  const [text, setText] = useState<string | null>(null)
  const [query, setQuery] = useState('')
  const [open, setOpen] = useState(false)
  const [activeIndex, setActiveIndex] = useState(0)
  const phone = usePhone()
  const [sheetOpen, setSheetOpen] = useState(false)

  useEffect(() => {
    const timer = setTimeout(() => setQuery(text ?? ''), SEARCH_DEBOUNCE_MS)
    return () => clearTimeout(timer)
  }, [text])

  const tags = useContainerImageTags(serviceId, container, query)
  const response = tags.data
  const isEcr = response?.source === 'ecr'
  const isFreeText = response?.source === 'none'
  const options = isEcr ? response.tags : []
  const sheetMode = phone && isEcr && response.total > SHEET_TAG_THRESHOLD
  const listOpen = open && isEcr && !disabled && !sheetMode
  const searching = tags.isPlaceholderData
  const unfiltered = query.trim() === '' && !searching

  const freeTextInvalid = isFreeText && text !== null && !IMAGE_TAG_RE.test(text)

  const choose = (tag: string) => {
    onChange(tag)
    setText(null)
    setOpen(false)
    setSheetOpen(false)
  }

  const closeSheet = () => {
    setSheetOpen(false)
    setText(null)
  }

  const onInput = (value: string) => {
    setText(value)
    setActiveIndex(0)
    if (isEcr) setOpen(true)
    else if (isFreeText && IMAGE_TAG_RE.test(value)) onChange(value)
  }

  const onKeyDown = (event: KeyboardEvent<HTMLInputElement>) => {
    // Opening on focus would reopen the sheet when focus returns to the field after it closes.
    if (sheetMode && (event.key === 'Enter' || event.key === 'ArrowDown')) {
      event.preventDefault()
      return setSheetOpen(true)
    }
    if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
      if (!isEcr) return
      event.preventDefault()
      if (!open) return setOpen(true)
      const step = event.key === 'ArrowDown' ? 1 : -1
      setActiveIndex((index) => (options.length === 0 ? 0 : (index + step + options.length) % options.length))
    } else if (event.key === 'Enter' && listOpen && options[activeIndex]) {
      event.preventDefault()
      choose(options[activeIndex].tag)
    } else if (event.key === 'Escape' && open) {
      event.preventDefault()
      setOpen(false)
      setText(null)
    }
  }

  const footer = !response
    ? ''
    : unfiltered
      ? `${options.length} of ${response.total} tags`
      : `${response.matched} of ${response.total} tags match${response.matched > options.length ? ` · newest ${options.length} shown` : ''}`

  return (
    <div className={styles.picker}>
      <input
        className={`${form.input} ${styles.pickerInput} ${freeTextInvalid ? styles.invalid : ''}`.trim()}
        role="combobox"
        aria-label="Image tag"
        aria-autocomplete="list"
        aria-expanded={listOpen}
        aria-controls={listOpen ? listboxId : undefined}
        aria-activedescendant={listOpen && options[activeIndex] ? `${listboxId}-${activeIndex}` : undefined}
        aria-invalid={freeTextInvalid || undefined}
        autoComplete="off"
        spellCheck={false}
        disabled={disabled}
        aria-haspopup={sheetMode ? 'dialog' : undefined}
        readOnly={sheetMode || undefined}
        value={text ?? currentTag}
        onChange={(event) => onInput(event.target.value)}
        onFocus={() => !sheetMode && isEcr && setOpen(true)}
        onClick={() => (sheetMode ? setSheetOpen(true) : isEcr && setOpen(true))}
        onBlur={() => {
          setOpen(false)
          if (isEcr) setText(null)
        }}
        onKeyDown={onKeyDown}
      />
      {isFreeText && (
        <p className={form.hint}>These images are not in ECR, so their tags cannot be listed. Type the tag to deploy.</p>
      )}
      {freeTextInvalid && (
        <p className={form.error} role="alert">
          Tag may contain letters, digits, . _ - and must start with a letter, digit or _ (128 characters at most).
        </p>
      )}
      {sheetMode && !disabled && (
        <BottomSheet open={sheetOpen} onClose={closeSheet} label="Image tag" title="Image tag" sub={footer} tall>
          <input
            type="search"
            className={`${form.input} ${styles.pickerInput}`}
            aria-label="Search image tags"
            placeholder="Search tags"
            autoComplete="off"
            spellCheck={false}
            value={text ?? ''}
            onChange={(event) => setText(event.target.value)}
          />
          <ul className={`${styles.sheetList}`} role="listbox" aria-label="Image tags" aria-busy={searching || undefined}>
            {options.map((option, index) => (
              <li key={option.tag} role="option" aria-selected={option.tag === currentTag} className={`${styles.option} ${styles.sheetOption}`} onClick={() => choose(option.tag)}>
                <span className={styles.tagName}>{option.tag}</span>
                {unfiltered && index === 0 && <span className={styles.mark}>newest</span>}
                {option.tag === deployedTag && <span className={`${styles.mark} ${styles.markDeployed}`}>deployed</span>}
                <span className={styles.pushed}>
                  <Ago ts={option.pushed_at} />
                </span>
              </li>
            ))}
            {options.length === 0 && !searching && <li className={styles.noMatches}>No tags match.</li>}
          </ul>
        </BottomSheet>
      )}
      {listOpen && (
        <ul className={styles.listbox} role="listbox" id={listboxId} aria-label="Image tags" aria-busy={searching || undefined}>
          {options.map((option, index) => (
            <li
              key={option.tag}
              id={`${listboxId}-${index}`}
              role="option"
              aria-selected={option.tag === currentTag}
              data-active={index === activeIndex}
              className={styles.option}
              onMouseDown={(event) => event.preventDefault()}
              onMouseEnter={() => setActiveIndex(index)}
              onClick={() => choose(option.tag)}
            >
              <span className={styles.tagName}>{option.tag}</span>
              {unfiltered && index === 0 && <span className={styles.mark}>newest</span>}
              {option.tag === deployedTag && <span className={`${styles.mark} ${styles.markDeployed}`}>deployed</span>}
              <span className={styles.pushed}>
                <Ago ts={option.pushed_at} />
              </span>
            </li>
          ))}
          {options.length === 0 && !searching && <li className={styles.noMatches}>No tags match.</li>}
          <li className={styles.optionFoot} role="presentation">
            {footer}
          </li>
        </ul>
      )}
    </div>
  )
}
