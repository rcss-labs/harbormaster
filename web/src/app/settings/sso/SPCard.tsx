import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { api } from '../../../api/client'
import type { SSOConfigDTO } from '../../../api/types'
import { Panel } from '../../../components/Panel'
import { ConfirmDialog } from '../../../components/ConfirmDialog'
import { KV } from '../../../components/KV'
import { useToast } from '../../../components/Toast'
import styles from './sso.module.css'

const UNDECRYPTABLE =
  'The SP key cannot be decrypted (HM_SESSION_KEY changed). Generate a new certificate and update your identity provider.'

export function SPCard({ config }: { config: SSOConfigDTO }) {
  const queryClient = useQueryClient()
  const toast = useToast()
  const [confirming, setConfirming] = useState(false)

  const rotate = useMutation({
    mutationFn: () => api.post<SSOConfigDTO>('/api/sso/key'),
    onSuccess: (updated) => {
      queryClient.setQueryData(['sso'], updated)
      toast.show('New certificate generated.', 'ok')
    },
    onError: (error) => toast.show(error.message, 'bad'),
  })

  const cert = config.sp_cert
  // The server may already list this among the warnings; the Status card shows those.
  const showUndecryptable = config.sp_key_state === 'undecryptable' && !config.warnings.includes(UNDECRYPTABLE)

  return (
    <Panel title="Service provider">
      <div className={styles.stack}>
        <KV
          rows={[
            ['Entity ID', <span className={styles.mono}>{config.sp_entity_id}</span>],
            ['ACS URL', <span className={styles.mono}>{config.acs_url}</span>],
            [
              'Certificate',
              cert ? (
                <div>
                  <div>{cert.subject}</div>
                  <div className={styles.mono}>{cert.fingerprint}</div>
                  <div className={styles.caption}>Expires {new Date(cert.not_after).toLocaleDateString()}</div>
                </div>
              ) : (
                'Created when you first save the identity provider.'
              ),
            ],
          ]}
        />
        {showUndecryptable && (
          <p role="alert" className={styles.err}>
            {UNDECRYPTABLE}
          </p>
        )}
        <div className={styles.row}>
          <a className={styles.btn} href={config.metadata_path} download>
            Download SP metadata
          </a>
          <button type="button" className={styles.btn} disabled={!config.configured || rotate.isPending} onClick={() => setConfirming(true)}>
            Generate new certificate
          </button>
        </div>
      </div>
      {confirming && (
        <ConfirmDialog
          title="Generate new certificate?"
          sub="The current certificate keeps decrypting assertions for 7 days. Update your identity provider with the new SP metadata."
          confirmLabel="Generate new certificate"
          onCancel={() => setConfirming(false)}
          onConfirm={() => {
            setConfirming(false)
            rotate.mutate()
          }}
        />
      )}
    </Panel>
  )
}
