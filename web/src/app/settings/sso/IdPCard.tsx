import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { api } from '../../../api/client'
import type { SSOConfigDTO } from '../../../api/types'
import { Ago } from '../../../components/Ago'
import { Banner } from '../../../components/Banner'
import { Card } from '../../../components/Card'
import { KV } from '../../../components/KV'
import { Seg } from '../../../components/Seg'
import { useToast } from '../../../components/Toast'
import { configBody } from './configBody'
import styles from './sso.module.css'

type Mode = 'url' | 'file' | 'manual'

const ENTITY_ID_CHANGE_WARNING =
  'Changing the identity provider signs out every SSO user and detaches every linked and pending SSO identity. Users keep their permissions and must be linked again, or will be created again at their next sign-in.'

const PEM_BLOCK = /-----BEGIN CERTIFICATE-----[\s\S]+?-----END CERTIFICATE-----/g

/** Splits pasted text into PEM blocks; text with no block is sent whole so the server explains what is wrong. */
function parseCerts(text: string): string[] {
  const trimmed = text.trim()
  if (trimmed === '') return []
  return trimmed.match(PEM_BLOCK) ?? [trimmed]
}

export function IdPCard({ config }: { config: SSOConfigDTO }) {
  const queryClient = useQueryClient()
  const toast = useToast()
  const [mode, setMode] = useState<Mode>('url')
  const [metadataUrl, setMetadataUrl] = useState('')
  const [file, setFile] = useState<File | null>(null)
  // Null until the admin edits; until then the manual fields follow the saved configuration.
  const [entityId, setEntityId] = useState<string | null>(null)
  const [ssoUrl, setSsoUrl] = useState<string | null>(null)
  const [pem, setPem] = useState('')
  const [error, setError] = useState('')

  const saved = (updated: SSOConfigDTO) => {
    queryClient.setQueryData(['sso'], updated)
    setError('')
    setEntityId(null)
    setSsoUrl(null)
    setPem('')
    toast.show('Saved.', 'ok')
  }
  const failed = (caught: Error) => setError(caught.message)

  const importMetadata = useMutation({
    mutationFn: (body: { url: string } | { xml: string }) => api.post<SSOConfigDTO>('/api/sso/metadata', body),
    onSuccess: saved,
    onError: failed,
  })
  const saveManual = useMutation({
    mutationFn: (body: Record<string, unknown>) => api.put<SSOConfigDTO>('/api/sso', body),
    onSuccess: saved,
    onError: failed,
  })

  const entityDraft = entityId ?? config.idp_entity_id
  const ssoUrlDraft = ssoUrl ?? config.idp_sso_redirect_url
  const entityChanged = config.idp_entity_id !== '' && entityDraft !== config.idp_entity_id
  const busy = importMetadata.isPending || saveManual.isPending

  const upload = async () => {
    if (!file) return
    importMetadata.mutate({ xml: await file.text() })
  }

  const saveManualFields = () => {
    const certs = parseCerts(pem)
    saveManual.mutate(
      configBody(config, {
        idp_entity_id: entityDraft.trim(),
        idp_sso_redirect_url: ssoUrlDraft.trim(),
        ...(certs.length > 0 ? { idp_certs: certs } : {}),
      }),
    )
  }

  return (
    <Card title="Identity provider">
      <div className={styles.stack}>
        <Seg
          label="Import mode"
          value={mode}
          onChange={setMode}
          options={[
            { value: 'url', label: 'Metadata URL' },
            { value: 'file', label: 'Metadata file' },
            { value: 'manual', label: 'Manual' },
          ]}
        />
        {mode === 'url' && (
          <div className={styles.row}>
            <label className={`${styles.field} ${styles.grow}`}>
              <span>Metadata URL</span>
              <input
                className={styles.ctl}
                type="url"
                placeholder="https://idp.example.com/metadata"
                value={metadataUrl}
                onChange={(event) => setMetadataUrl(event.target.value)}
              />
            </label>
            <button
              type="button"
              className={`${styles.btn} ${styles.primary}`}
              disabled={busy || metadataUrl.trim() === ''}
              onClick={() => importMetadata.mutate({ url: metadataUrl.trim() })}
            >
              Fetch
            </button>
          </div>
        )}
        {mode === 'file' && (
          <div className={styles.row}>
            <label className={styles.field}>
              <span>Metadata file</span>
              <input type="file" accept=".xml" onChange={(event) => setFile(event.target.files?.[0] ?? null)} />
            </label>
            <button type="button" className={`${styles.btn} ${styles.primary}`} disabled={busy || !file} onClick={() => void upload()}>
              Upload
            </button>
          </div>
        )}
        {mode === 'manual' && (
          <div className={styles.form}>
            <label className={styles.field}>
              <span>Entity ID</span>
              <input className={styles.ctl} value={entityDraft} onChange={(event) => setEntityId(event.target.value)} />
            </label>
            {entityChanged && <Banner kind="warn">{ENTITY_ID_CHANGE_WARNING}</Banner>}
            <label className={styles.field}>
              <span>SSO URL (HTTP-Redirect)</span>
              <input className={styles.ctl} value={ssoUrlDraft} onChange={(event) => setSsoUrl(event.target.value)} />
            </label>
            <label className={styles.field}>
              <span>Certificate PEM</span>
              <textarea
                className={styles.ctl}
                rows={6}
                placeholder="-----BEGIN CERTIFICATE-----"
                value={pem}
                onChange={(event) => setPem(event.target.value)}
              />
            </label>
            {config.configured && <p className={styles.hint}>Leave the certificate empty to keep the stored certificates.</p>}
            <div className={styles.row}>
              <button type="button" className={`${styles.btn} ${styles.primary}`} disabled={busy} onClick={saveManualFields}>
                Save identity provider
              </button>
            </div>
          </div>
        )}
        {error && (
          <p role="alert" className={styles.err}>
            {error}
          </p>
        )}
        {config.idp_entity_id !== '' && (
          <>
            <KV
              rows={[
                ['Entity ID', <span className={styles.mono}>{config.idp_entity_id}</span>],
                ['SSO URL', <span className={styles.mono}>{config.idp_sso_redirect_url}</span>],
                [
                  'Certificates',
                  config.idp_certs.length === 0 ? (
                    'None'
                  ) : (
                    <ul className={styles.certs}>
                      {config.idp_certs.map((cert) => (
                        <li key={cert.fingerprint}>
                          <div>{cert.subject}</div>
                          <div className={styles.mono}>{cert.fingerprint}</div>
                          <div className={styles.caption}>Expires {new Date(cert.not_after).toLocaleDateString()}</div>
                        </li>
                      ))}
                    </ul>
                  ),
                ],
              ]}
            />
            {config.metadata_url && config.metadata_fetched_at && (
              <div className={styles.row}>
                <span className={styles.caption}>
                  Last fetched <Ago ts={config.metadata_fetched_at} />
                </span>
                <button
                  type="button"
                  className={styles.btn}
                  disabled={busy}
                  onClick={() => importMetadata.mutate({ url: config.metadata_url ?? '' })}
                >
                  Refresh
                </button>
              </div>
            )}
          </>
        )}
      </div>
    </Card>
  )
}
