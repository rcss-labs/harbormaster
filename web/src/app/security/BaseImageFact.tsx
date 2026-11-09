import { useState, type FormEvent } from 'react'
import { useSetBaseImage, type SecurityImageDetail } from '../../api/security'
import { useToast } from '../../components/Toast'
import { SECURITY_COPY } from './copy'
import styles from './security.module.css'

const MAX_REF_LENGTH = 255

/** The reason a reference cannot be sent, or null. An empty reference is valid only as "clear". The server stays the authority. */
function refProblem(ref: string): string | null {
  if (ref === '') return SECURITY_COPY.baseImageEmpty
  if (/\s/.test(ref)) return SECURITY_COPY.baseImageSpaces
  return ref.length > MAX_REF_LENGTH ? SECURITY_COPY.baseImageTooLong : null
}

/** The "Base image" line of the image facts: what is set, how it compared with the layers, and the inline editor. */
export function BaseImageFact({ image }: { image: SecurityImageDetail }) {
  const toast = useToast()
  const setBase = useSetBaseImage(image.digest)
  const base = image.base_image
  const [editing, setEditing] = useState(false)
  const [value, setValue] = useState('')
  const [problem, setProblem] = useState<string | null>(null)

  const startEditing = () => {
    setValue(base.ref)
    setProblem(null)
    setEditing(true)
  }

  const save = (ref: string) =>
    setBase.mutate(ref, {
      onSuccess: () => {
        toast.show(ref === '' ? SECURITY_COPY.baseImageCleared : SECURITY_COPY.baseImageSaved, 'ok')
        setEditing(false)
      },
      onError: (error) => toast.show(error instanceof Error ? error.message : SECURITY_COPY.baseImageFailed, 'bad'),
    })

  const submit = (event: FormEvent) => {
    event.preventDefault()
    const ref = value.trim()
    const found = refProblem(ref)
    setProblem(found)
    if (!found) save(ref)
  }

  if (editing) {
    return (
      <form className={styles.baseForm} onSubmit={submit} noValidate>
        <label htmlFor="base-image-ref">{SECURITY_COPY.baseImageLabel}</label>
        <input
          id="base-image-ref"
          value={value}
          placeholder={SECURITY_COPY.baseImagePlaceholder}
          autoComplete="off"
          spellCheck={false}
          aria-invalid={problem !== null}
          onChange={(event) => setValue(event.target.value)}
        />
        <span className={styles.sub}>{SECURITY_COPY.baseImageHelp(image.repository)}</span>
        {problem && <p className={styles.fieldError}>{problem}</p>}
        <span className={styles.actions}>
          <button type="submit" className={styles.btn} disabled={setBase.isPending}>
            {SECURITY_COPY.saveBaseImage}
          </button>
          <button type="button" className={styles.btn} onClick={() => setEditing(false)}>
            {SECURITY_COPY.cancelBaseImage}
          </button>
          {base.ref !== '' && (
            <button type="button" className={styles.btn} disabled={setBase.isPending} onClick={() => save('')}>
              {SECURITY_COPY.clearBaseImage}
            </button>
          )}
        </span>
      </form>
    )
  }

  const stateNote =
    base.state === 'matched' ? (
      <span className={styles.sub}>{SECURITY_COPY.baseImageMatched(base.matched_layers, base.total_layers)}</span>
    ) : base.state === 'pending' ? (
      <span className={styles.sub}>{SECURITY_COPY.baseImagePending}</span>
    ) : base.state === 'mismatch' ? (
      <span className={styles.baseWarning}>{base.message}</span>
    ) : base.state === 'error' ? (
      <span className={styles.baseDanger}>{base.message}</span>
    ) : null

  return (
    <span className={styles.baseImage}>
      <span className={styles.baseImageLine}>
        <span>{base.state === 'none' ? SECURITY_COPY.baseImageNotSet : SECURITY_COPY.baseImageLine(base.ref)}</span>
        {image.can.set_base && (
          <button type="button" className={styles.btn} onClick={startEditing}>
            {base.state === 'none' ? SECURITY_COPY.setBaseImage : SECURITY_COPY.changeBaseImage}
          </button>
        )}
      </span>
      {stateNote}
    </span>
  )
}
