import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { api } from '../../../api/client'
import type { RegionResult } from '../../../api/types'
import { Dialog } from '../../../components/Dialog'
import { useToast } from '../../../components/Toast'
import { COMMERCIAL_REGIONS, DEFAULT_REGIONS, invalidateAccountQueries } from './accountApi'
import { RegionCheckboxes } from './RegionCheckboxes'
import styles from './accounts.module.css'

const FIX_AND_RETRY = 'Fix the role and test again.'

interface ConnectAccountDialogProps {
  onClose: () => void
}

interface ConnectOutcome {
  regions: RegionResult[]
  connected: boolean
}

/** Tests the role in every selected region and, only when all pass, saves the account. */
export function ConnectAccountDialog({ onClose }: ConnectAccountDialogProps) {
  const queryClient = useQueryClient()
  const toast = useToast()
  const [name, setName] = useState('')
  const [roleArn, setRoleArn] = useState('')
  const [regions, setRegions] = useState<string[]>(DEFAULT_REGIONS)
  const [results, setResults] = useState<RegionResult[]>([])
  const [error, setError] = useState('')
  const externalIdQuery = useQuery({
    queryKey: ['external-id'],
    queryFn: () => api.get<{ external_id: string }>('/api/accounts/external-id'),
  })
  const externalId = externalIdQuery.data?.external_id ?? ''

  const connect = useMutation<ConnectOutcome>({
    mutationFn: async () => {
      // Keep the list order so the test and the saved account match the checkbox order.
      const selected = COMMERCIAL_REGIONS.filter((region) => regions.includes(region))
      const trimmedRole = roleArn.trim()
      const test = await api.post<{ regions: RegionResult[] }>('/api/accounts/test', {
        role_arn: trimmedRole,
        external_id: externalId,
        regions: selected,
      })
      setResults(test.regions)
      const allOk = test.regions.every((result) => result.ok)
      if (!allOk) return { regions: test.regions, connected: false }
      await api.post('/api/accounts', { name: name.trim(), role_arn: trimmedRole, external_id: externalId, regions: selected })
      return { regions: test.regions, connected: true }
    },
    onMutate: () => {
      setError('')
    },
    onSuccess: ({ connected }) => {
      if (!connected) {
        setError(FIX_AND_RETRY)
        return
      }
      invalidateAccountQueries(queryClient)
      toast.show(
        <>
          Connected <strong>{name.trim()}</strong>.
        </>,
        'ok',
      )
      onClose()
    },
    onError: (caught) => setError(caught.message),
  })

  return (
    <Dialog
      title="Connect an AWS account"
      sub="Harbormaster assumes this role from its own task role."
      onClose={onClose}
      actions={[
        { label: 'Cancel', onClick: onClose },
        {
          label: 'Test and connect',
          kind: 'primary',
          onClick: () => connect.mutate(),
          disabled: connect.isPending || externalId === '' || regions.length === 0,
        },
      ]}
    >
      <label className={styles.field}>
        <span>Display name</span>
        <input className={styles.ctl} placeholder="production" value={name} onChange={(event) => setName(event.target.value)} />
      </label>
      <label className={styles.field}>
        <span>Role ARN</span>
        <input
          className={`${styles.ctl} ${styles.mono}`}
          placeholder="arn:aws:iam::123456789012:role/harbormaster-access"
          value={roleArn}
          onChange={(event) => setRoleArn(event.target.value)}
        />
      </label>
      <label className={styles.field}>
        <span>External ID</span>
        <input className={`${styles.ctl} ${styles.mono}`} value={externalId} readOnly />
        <span className={styles.hint}>Put this in the role's trust policy as sts:ExternalId.</span>
      </label>
      <RegionCheckboxes selected={regions} onChange={setRegions} />
      {results.length > 0 && (
        <div className={styles.results}>
          {results.map((result) =>
            result.ok ? (
              <div key={result.region} className={styles.resultOk}>
                {`✓ ${result.region} · ${result.clusters} clusters`}
              </div>
            ) : (
              <div key={result.region} className={styles.resultBad}>
                {`✗ ${result.region} · ${result.error}`}
              </div>
            ),
          )}
        </div>
      )}
      <p className={styles.hint}>
        The role needs read access to ECS, EC2 (VPCs and subnets), EFS, CloudWatch and Logs, plus{' '}
        <span className={styles.mono}>ecs:RegisterTaskDefinition</span>, <span className={styles.mono}>ecs:UpdateService</span>,{' '}
        <span className={styles.mono}>ecs:StopTask</span>, <span className={styles.mono}>ecs:ExecuteCommand</span> and{' '}
        <span className={styles.mono}>iam:PassRole</span> for task roles.
      </p>
      <div className={styles.err} role="alert">
        {error}
      </div>
    </Dialog>
  )
}
