import { useState } from 'react'
import { createPortal } from 'react-dom'
import {
  useAgentSettings,
  useAgentUsage,
  useSaveAgentSettings,
  useTestAgentModel,
  type AgentSettingsDTO,
  type AgentTestResult,
} from '../../../api/agent'
import { Banner } from '../../../components/Banner'
import { ConfirmDialog } from '../../../components/ConfirmDialog'
import { Meter } from '../../../components/Meter'
import { Panel } from '../../../components/Panel'
import { Pill } from '../../../components/Pill'
import { useToast } from '../../../components/Toast'
import { usePhone } from '../../../components/usePhone'
import { HeaderAction } from '../HeaderAction'
import { useStickyBar } from '../useStickyBar'
import styles from '../settings.module.css'
import { UsageTable } from './UsageTable'

// Where an operator finds how providers are set up.
const INSTALL_GUIDE_URL = 'https://github.com/rcss-labs/harbormaster/blob/main/harbormaster-docs/docs/install/deploy-with-terraform.md'

const PROVIDER_NAMES: Record<string, string> = {
  anthropic: 'Anthropic',
  openai: 'OpenAI',
  google: 'Google',
  bedrock: 'Amazon Bedrock',
  fake: 'Fake provider',
}
const providerName = (id: string) => PROVIDER_NAMES[id] ?? id

const ALLOWANCE_PATTERN = /^\d+(\.\d{1,2})?$/

/** Prices arrive as micro-dollars per million tokens. */
function dollarsPerMillion(micro: number): string {
  const dollars = micro / 1_000_000
  return `$${Number.isInteger(dollars * 100) ? dollars.toFixed(2) : String(dollars)}`
}

interface Draft {
  budget: string
  provider: string
  model: string
  unredacted: string[]
}

function baselineOf(settings: AgentSettingsDTO): Draft {
  const chat = settings.role_models.chat
  return {
    budget: settings.monthly_budget_usd,
    provider: chat?.provider ?? '',
    // A model the operator dropped is not carried into the draft, so the admin has to choose again.
    model: chat?.available ? chat.model : '',
    unredacted: settings.unredacted_providers,
  }
}

const sameList = (a: string[], b: string[]) => a.length === b.length && a.every((item) => b.includes(item))

export function AgentSettings() {
  const settings = useAgentSettings()
  const usage = useAgentUsage()
  const save = useSaveAgentSettings()
  const test = useTestAgentModel()
  const toast = useToast()
  const phone = usePhone()
  useStickyBar(phone)
  const [edited, setEdited] = useState<Draft | null>(null)
  const [testResult, setTestResult] = useState<AgentTestResult | null>(null)
  const [liftingFor, setLiftingFor] = useState<string | null>(null)

  const data = settings.data
  const baseline = data ? baselineOf(data) : null
  const draft = edited ?? baseline
  const patch = (change: Partial<Draft>) => {
    if (!draft) return
    setEdited({ ...draft, ...change })
    setTestResult(null)
  }

  if (settings.isError) {
    return <Panel title="AI agent" error={{ message: 'Could not load the agent settings.', detail: settings.error.message, onRetry: () => void settings.refetch() }}>{null}</Panel>
  }
  if (!data || !draft || !baseline) return <Panel title="AI agent" loading>{null}</Panel>

  const providers = data.catalogue.providers
  const chat = data.role_models.chat
  const staleModel = chat !== undefined && !chat.available
  const models = providers.find((entry) => entry.provider === draft.provider)?.models ?? []
  const chosenModel = models.find((model) => model.id === draft.model)
  const budgetValid = ALLOWANCE_PATTERN.test(draft.budget.trim())
  const dirty = draft.budget !== baseline.budget || draft.provider !== baseline.provider || draft.model !== baseline.model || !sameList(draft.unredacted, baseline.unredacted)
  const canSave = dirty && budgetValid && draft.model !== '' && !save.isPending

  const settled = Number(data.spend.settled_usd)
  const reserved = Number(data.spend.reserved_usd)
  const budget = Number(data.monthly_budget_usd)
  const usedPct = budget > 0 ? ((settled + reserved) / budget) * 100 : 0

  const submit = () => {
    save.mutate(
      {
        monthly_budget_usd: draft.budget.trim(),
        unredacted_providers: draft.unredacted,
        role_models: { chat: { provider: draft.provider, model: draft.model } },
      },
      {
        onSuccess: () => {
          setEdited(null)
          setTestResult(null)
          toast.show('Saved.', 'ok')
        },
        onError: (error) => toast.show(error.message, 'bad'),
      },
    )
  }

  const runTest = () => {
    setTestResult(null)
    test.mutate(
      { provider: draft.provider, model: draft.model },
      {
        onSuccess: setTestResult,
        onError: (error) => setTestResult({ ok: false, kind: 'error', message: error.message }),
      },
    )
  }

  const toggleUnredacted = (provider: string, on: boolean) => {
    if (on) setLiftingFor(provider)
    else patch({ unredacted: draft.unredacted.filter((entry) => entry !== provider) })
  }

  const saveButton = (
    <button type="button" className={`${styles.btn} ${styles.primary}`} disabled={!canSave} onClick={submit}>
      Save
    </button>
  )

  return (
    <>
      {phone ? (
        createPortal(
          <div className={`hm-bar ${styles.saveBar}`} data-hm-bar="">
            <span className={styles.barNote}>{dirty ? 'Unsaved changes' : 'No changes'}</span>
            {saveButton}
          </div>,
          document.body,
        )
      ) : (
        <HeaderAction>{saveButton}</HeaderAction>
      )}

      <Panel title="Status">
        <p>
          {data.agent_reachable ? (
            <>
              <Pill kind="ok">Reachable</Pill> The agent is reachable.
            </>
          ) : (
            <>
              <Pill kind="bad">Not reachable</Pill> The agent is not reachable. Chat and model changes wait until it is back.
            </>
          )}
        </p>
        {providers.length === 0 ? (
          <p>
            No model provider is enabled. Your operator sets providers up at install time.{' '}
            <a href={INSTALL_GUIDE_URL} target="_blank" rel="noreferrer">
              Install guide
            </a>
          </p>
        ) : (
          <p className={styles.hint}>Providers your operator enabled: {providers.map((entry) => providerName(entry.provider)).join(', ')}.</p>
        )}
      </Panel>

      {providers.length > 0 && (
        <>
          <Panel title="Model for chat">
            {staleModel && (
              <Banner kind="warn">This model is no longer offered by the operator. Choose another.</Banner>
            )}
            <div className={styles.windowRow}>
              <label htmlFor="agent-provider">Provider</label>
              <select
                id="agent-provider"
                value={draft.provider}
                onChange={(event) => patch({ provider: event.target.value, model: '' })}
              >
                <option value="">Choose a provider</option>
                {providers.map((entry) => (
                  <option key={entry.provider} value={entry.provider}>
                    {providerName(entry.provider)}
                  </option>
                ))}
              </select>
              <label htmlFor="agent-model">Model</label>
              <select id="agent-model" value={draft.model} disabled={draft.provider === ''} onChange={(event) => patch({ model: event.target.value })}>
                <option value="">Choose a model</option>
                {models.map((model) => (
                  <option key={model.id} value={model.id}>
                    {model.id}
                  </option>
                ))}
              </select>
              <button type="button" className={styles.btn} disabled={draft.model === '' || test.isPending} onClick={runTest}>
                Test model
              </button>
            </div>
            {chosenModel && (
              <p className={styles.hint}>
                {`Input ${dollarsPerMillion(chosenModel.input_per_mtok_micro_usd)} per million tokens, output ${dollarsPerMillion(chosenModel.output_per_mtok_micro_usd)} per million tokens.`}
              </p>
            )}
            {testResult && (
              <p role="status" className={testResult.ok ? styles.hint : styles.error}>
                {testResult.ok ? testResult.message || 'The model answered.' : testResult.message}
              </p>
            )}
          </Panel>

          <Panel title="Monthly allowance">
            <div className={styles.windowRow}>
              <span>$</span>
              <input
                type="text"
                inputMode="decimal"
                aria-label="Monthly allowance in US dollars"
                className={styles.windowInput}
                value={draft.budget}
                onChange={(event) => patch({ budget: event.target.value })}
              />
              <span>per month</span>
            </div>
            {!budgetValid && (
              <p role="alert" className={styles.error}>
                Enter an amount in dollars, such as 25.00.
              </p>
            )}
            <p>{`Spent $${data.spend.settled_usd} and reserved $${data.spend.reserved_usd} of $${data.monthly_budget_usd} in ${data.spend.month}.`}</p>
            <Meter pct={usedPct} color={usedPct >= 90 ? 'var(--danger)' : 'var(--accent)'} />
          </Panel>

          <Panel title="Sensitive values">
            {providers.map((entry) => (
              <label key={entry.provider} className={styles.windowRow}>
                <input
                  type="checkbox"
                  checked={draft.unredacted.includes(entry.provider)}
                  onChange={(event) => toggleUnredacted(entry.provider, event.target.checked)}
                />
                <span>{`Send environment values to ${providerName(entry.provider)}`}</span>
              </label>
            ))}
            <p className={styles.hint}>Values that look like secrets are always hidden.</p>
          </Panel>
        </>
      )}

      <UsageTable usage={usage.data} loading={usage.isPending} error={usage.isError} />

      {liftingFor && (
        <ConfirmDialog
          title={`Send environment values to ${providerName(liftingFor)}?`}
          sub="Environment values in task definitions will leave Harbormaster and go to this provider when someone asks the agent about them."
          confirmLabel="Send environment values"
          onConfirm={() => {
            patch({ unredacted: [...draft.unredacted, liftingFor] })
            setLiftingFor(null)
          }}
          onCancel={() => setLiftingFor(null)}
        />
      )}
    </>
  )
}
