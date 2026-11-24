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
import { ApiError } from '../../../api/client'
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

// The install guide lives in the repository docs; the app has no docs site of its own.
const INSTALL_GUIDE_URL = 'https://github.com/rcss-labs/harbormaster/blob/main/harbormaster-docs/docs/install/ai-agent.md'

const PROVIDER_NAMES: Record<string, string> = {
  anthropic: 'Anthropic',
  openai: 'OpenAI',
  google: 'Google',
  bedrock: 'Amazon Bedrock',
  fake: 'Fake provider',
}
const providerName = (id: string) => PROVIDER_NAMES[id] ?? id

const ALLOWANCE_PATTERN = /^\d+(\.\d{1,2})?$/

/** Prices arrive as micro-dollars per million tokens; integer arithmetic keeps them exact. */
function dollarsPerMillion(micro: number): string {
  const whole = Math.floor(micro / 1_000_000)
  const fraction = String(micro % 1_000_000)
    .padStart(6, '0')
    .replace(/0+$/, '')
    .padEnd(2, '0')
  return `$${whole.toLocaleString('en-US')}.${fraction}`
}

interface Draft {
  budget: string
  provider: string
  model: string
  unredacted: string[]
}

type TestOutcome = AgentTestResult & { provider: string; model: string }

function baselineOf(settings: AgentSettingsDTO): Draft {
  const chat = settings.role_models.chat
  return {
    budget: settings.monthly_budget_usd,
    // A model the operator dropped stays in the draft: it is resubmitted unchanged until the admin chooses another.
    provider: chat?.provider ?? '',
    model: chat?.model ?? '',
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
  const [testResult, setTestResult] = useState<TestOutcome | null>(null)
  const [liftingFor, setLiftingFor] = useState<string | null>(null)
  const [conflict, setConflict] = useState<string | null>(null)

  const data = settings.data
  const baseline = data ? baselineOf(data) : null
  const draft = edited ?? baseline
  const patch = (change: Partial<Draft>) => {
    if (draft) setEdited({ ...draft, ...change })
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
  // A provider without a model is half a choice; no provider at all is fine and leaves chat out of the request.
  const modelChosen = draft.provider === '' || draft.model !== ''
  const canSave = dirty && budgetValid && modelChosen && !save.isPending
  const stillStale = staleModel && draft.provider === baseline.provider && draft.model === baseline.model

  const settled = Number(data.spend.settled_usd)
  const reserved = Number(data.spend.reserved_usd)
  const budget = Number(data.monthly_budget_usd)
  const usedPct = budget > 0 ? ((settled + reserved) / budget) * 100 : 0

  // Providers shown in the redaction section: the catalogue plus any stored one the operator no longer offers.
  const redactionProviders = [...providers.map((entry) => entry.provider), ...draft.unredacted.filter((id) => !providers.some((entry) => entry.provider === id))]

  const submit = () => {
    save.mutate(
      {
        version: data.version,
        monthly_budget_usd: draft.budget.trim(),
        unredacted_providers: draft.unredacted,
        role_models: draft.provider !== '' && draft.model !== '' ? { chat: { provider: draft.provider, model: draft.model } } : {},
      },
      {
        onSuccess: () => {
          setEdited(null)
          setTestResult(null)
          setConflict(null)
          toast.show('Saved.', 'ok')
        },
        onError: (error) => {
          if (error instanceof ApiError && error.code === 'settings_changed') {
            // Load what is stored now. The fields the admin changed stay as typed; the others follow the stored
            // values, so a save does not quietly put back what someone else just changed.
            setConflict(error.message)
            const mine = draft
            void settings.refetch().then((result) => {
              if (!result.data) return
              const stored = baselineOf(result.data)
              const modelTouched = mine.provider !== baseline.provider || mine.model !== baseline.model
              setEdited({
                budget: mine.budget !== baseline.budget ? mine.budget : stored.budget,
                provider: modelTouched ? mine.provider : stored.provider,
                model: modelTouched ? mine.model : stored.model,
                unredacted: sameList(mine.unredacted, baseline.unredacted) ? stored.unredacted : mine.unredacted,
              })
            })
          } else {
            toast.show(error.message, 'bad')
          }
        },
      },
    )
  }

  const runTest = () => {
    setTestResult(null)
    const tested = { provider: draft.provider, model: draft.model }
    test.mutate(tested, {
      onSuccess: (result) => setTestResult({ ...result, ...tested }),
      onError: (error) =>
        setTestResult({ ok: false, kind: 'error', message: error instanceof ApiError ? error.message : 'The test could not run. Try again.', ...tested }),
    })
  }

  const toggleUnredacted = (provider: string, on: boolean) => {
    if (on) setLiftingFor(provider)
    else patch({ unredacted: draft.unredacted.filter((entry) => entry !== provider) })
  }

  // A result that arrives after another model was chosen is not about the current choice.
  const shownResult = testResult && testResult.provider === draft.provider && testResult.model === draft.model ? testResult : null

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

      {conflict && (
        <Banner kind="warn">
          <span>{conflict}</span> The stored settings are loaded below; your changes are still in the form. Check them, then save again.
        </Banner>
      )}

      <Panel title="Status">
        <p>
          {data.agent_reachable ? (
            <>
              <Pill kind="ok">Reachable</Pill> The agent is reachable.
            </>
          ) : (
            <>
              <Pill kind="bad">Not reachable</Pill> The agent is not reachable. Allowance and usage still work; changing models or sensitive values waits until it is back.
            </>
          )}
        </p>
        {providers.length > 0 ? (
          <p className={styles.hint}>Providers your operator enabled: {providers.map((entry) => providerName(entry.provider)).join(', ')}.</p>
        ) : (
          data.agent_reachable && (
            <p>
              No model provider is enabled. Your operator sets providers up at install time.{' '}
              <a href={INSTALL_GUIDE_URL} target="_blank" rel="noreferrer">
                Install guide
              </a>
            </p>
          )
        )}
      </Panel>

      {providers.length > 0 && (
        <Panel title="Model for chat">
          {staleModel && stillStale && <Banner kind="warn">This model is no longer offered by the operator. Choose another.</Banner>}
          <div className={styles.windowRow}>
            <label htmlFor="agent-provider">Provider</label>
            <select id="agent-provider" className={styles.select} value={draft.provider} onChange={(event) => patch({ provider: event.target.value, model: '' })}>
              <option value="">Choose a provider</option>
              {providers.map((entry) => (
                <option key={entry.provider} value={entry.provider}>
                  {providerName(entry.provider)}
                </option>
              ))}
            </select>
            <label htmlFor="agent-model">Model</label>
            <select id="agent-model" className={styles.select} value={draft.model} disabled={draft.provider === ''} onChange={(event) => patch({ model: event.target.value })}>
              <option value="">Choose a model</option>
              {draft.model !== '' && !models.some((model) => model.id === draft.model) && <option value={draft.model}>{draft.model}</option>}
              {models.map((model) => (
                <option key={model.id} value={model.id}>
                  {model.id}
                </option>
              ))}
            </select>
            <button type="button" className={styles.btn} disabled={!chosenModel || test.isPending} onClick={runTest}>
              Test model
            </button>
          </div>
          <p className={styles.hint}>A test makes one small paid call.</p>
          {chosenModel && (
            <p className={styles.hint}>
              {`Input ${dollarsPerMillion(chosenModel.input_per_mtok_micro_usd)} per million tokens, output ${dollarsPerMillion(chosenModel.output_per_mtok_micro_usd)} per million tokens.`}
            </p>
          )}
          <p role="status" className={shownResult && !shownResult.ok ? styles.error : styles.hint}>
            {shownResult ? (shownResult.ok ? shownResult.message || 'The model answered.' : shownResult.message) : ''}
          </p>
        </Panel>
      )}

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

      {redactionProviders.length > 0 && (
        <Panel title="Sensitive values">
          {redactionProviders.map((provider) => (
            <label key={provider} className={styles.windowRow}>
              <input type="checkbox" checked={draft.unredacted.includes(provider)} onChange={(event) => toggleUnredacted(provider, event.target.checked)} />
              <span>{`Send environment values to ${providerName(provider)}`}</span>
            </label>
          ))}
          <p className={styles.hint}>Values that look like secrets are always hidden.</p>
        </Panel>
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
