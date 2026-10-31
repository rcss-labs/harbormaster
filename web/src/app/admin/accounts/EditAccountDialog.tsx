import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { api } from '../../../api/client'
import type { AccountDTO } from '../../../api/types'
import { Dialog } from '../../../components/Dialog'
import { useToast } from '../../../components/Toast'
import { COMMERCIAL_REGIONS, invalidateAccountQueries } from './accountApi'
import { RegionCheckboxes } from './RegionCheckboxes'
import styles from './accounts.module.css'

interface EditAccountDialogProps {
  account: AccountDTO
  onClose: () => void
}

/** Renames an account and changes which regions it scans. */
export function EditAccountDialog({ account, onClose }: EditAccountDialogProps) {
  const queryClient = useQueryClient()
  const toast = useToast()
  const [name, setName] = useState(account.name)
  const [regions, setRegions] = useState<string[]>(account.regions)
  const [error, setError] = useState('')

  const save = useMutation({
    mutationFn: () =>
      api.patch<void>(`/api/accounts/${account.id}`, {
        name: name.trim(),
        regions: COMMERCIAL_REGIONS.filter((region) => regions.includes(region)),
      }),
    onSuccess: () => {
      invalidateAccountQueries(queryClient)
      toast.show(
        <>
          Saved <strong>{name.trim()}</strong>.
        </>,
        'ok',
      )
      onClose()
    },
    onError: (caught) => setError(caught.message),
  })

  return (
    <Dialog
      title={`Edit ${account.name}`}
      onClose={onClose}
      actions={[
        { label: 'Cancel', onClick: onClose },
        { label: 'Save', kind: 'primary', onClick: () => save.mutate(), disabled: save.isPending },
      ]}
    >
      <label className={styles.field}>
        <span>Display name</span>
        <input className={styles.ctl} value={name} onChange={(event) => setName(event.target.value)} />
      </label>
      <RegionCheckboxes selected={regions} onChange={setRegions} />
      <div className={styles.err} role="alert">
        {error}
      </div>
    </Dialog>
  )
}
