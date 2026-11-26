import { useState } from 'react'
import { createPortal } from 'react-dom'
import {
  useAgentSettings,
  useAgentUsage,
  useSaveAgentSettings,
  useTestAgentModel,
  type AgentCatalogueModel,
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
import { IdentitySection } from './IdentitySection'
import { InvestigationsTable } from './InvestigationsTable'
import { ReviewsSection } from './ReviewsSection'
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
  investigatorProvider: string
  investigatorModel: string
  reviewerProvider: string
  reviewerModel: string
  unredacted: string[]
}

type Role = 'chat' | 'investigator' | 'reviewer'

type TestOutcome = AgentTestResult & { provider: string; model: string }

function baselineOf(settings: AgentSettingsDTO): Draft {
  const chat = settings.role_models.chat
  const investigator = settings.role_models.investigator
  const reviewer = settings.role_models.reviewer
  return {
    budget: settings.monthly_budget_usd,
    // A model the operator dropped stays in the draft: it is resubmitted unchanged until the admin chooses another.
    provider: chat?.provider ?? '',
    model: chat?.model ?? '',
    investigatorProvider: investigator?.provider ?? '',
    investigatorModel: investigator?.model ?? '',
    reviewerProvider: reviewer?.provider ?? '',
    reviewerModel: reviewer?.model ?? '',
    unredacted: settings.unredacted_providers,
  }
}

/** Per provider: what the admin ticked is added to the stored list and what they cleared is removed from it. */
function mergeUnredacted(loaded: string[], mine: string[], stored: string[]): string[] {
  const added = mine.filter((id) => !loaded.includes(id))
  const removed = loaded.filter((id) => !mine.includes(id))
  return [...new Set([...stored, ...added])].filter((id) => !removed.includes(id))
}

const sameList = (a: string[], b: string[]) => a.length === b.length && a.every((item) => b.includes(item))

interface ModelPanelProps {
  title: string
  /** Distinguishes the controls of the two panels for labels and ids. */
  name: string
  providers: { provider: string; models: AgentCatalogueModel[] }[]
  provider: string
  model: string
  stale: boolean
  onProvider: (provider: string) => void
  onModel: (model: string) => void
  result: TestOutcome | null
  testing: boolean
  onTest: () => void
}

/** Provider and model for one role, with a test call and the price of the chosen model. */
function ModelPanel(props: ModelPanelProps) {
  const { name, providers, provider, model } = props
  const models = providers.find((entry) => entry.provider === provider)?.models ?? []
  const chosen = models.find((entry) => entry.id === model)
  // A result that arrives after another model was chosen is not about the current choice.
  const shown = props.result && props.result.provider === provider && props.result.model === model ? props.result : null
  const prefix = name === '' ? '' : `${name} `
  return (
    <Panel title={props.title}>
      {props.stale && <Banner kind="warn">This model is no longer offered by the operator. Choose another.</Banner>}
      <div className={styles.windowRow}>
        <label htmlFor={`agent-${name}provider`}>{`${prefix}provider`.replace(/^./, (c) => c.toUpperCase())}</label>
        <select id={`agent-${name}provider`} className={styles.select} value={provider} onChange={(event) => props.onProvider(event.target.value)}>
          <option value="">Choose a provider</option>
          {providers.map((entry) => (
            <option key={entry.provider} value={entry.provider}>
              {providerName(entry.provider)}
            </option>
          ))}
        </select>
        <label htmlFor={`agent-${name}model`}>{`${prefix}model`.replace(/^./, (c) => c.toUpperCase())}</label>
        <select id={`agent-${name}model`} className={styles.select} value={model} disabled={provider === ''} onChange={(event) => props.onModel(event.target.value)}>
          <option value="">Choose a model</option>
          {model !== '' && !models.some((entry) => entry.id === model) && <option value={model}>{model}</option>}
          {models.map((entry) => (
            <option key={entry.id} value={entry.id}>
              {entry.id}
            </option>
          ))}
        </select>
        <button type="button" className={styles.btn} disabled={!chosen || props.testing} onClick={props.onTest}>
          {name === '' ? 'Test model' : `Test ${name} model`}
        </button>
      </div>
      {name === '' && <p className={styles.hint}>A test makes one small paid call.</p>}
      {chosen && (
        <p className={styles.hint}>
          {`Input ${dollarsPerMillion(chosen.input_per_mtok_micro_usd)} per million tokens, output ${dollarsPerMillion(chosen.output_per_mtok_micro_usd)} per million tokens.`}
        </p>
      )}
      <p role="status" className={shown && !shown.ok ? styles.error : styles.hint}>
        {shown ? (shown.ok ? shown.message || 'The model answered.' : shown.message) : ''}
      </p>
    </Panel>
  )
}

export function AgentSettings() {
  const settings = useAgentSettings()
  const usage = useAgentUsage()
  const save = useSaveAgentSettings()
  const test = useTestAgentModel()
  const toast = useToast()
  const phone = usePhone()
  useStickyBar(phone)
  const [edited, setEdited] = useState<Draft | null>(null)
  const [testResults, setTestResults] = useState<Partial<Record<Role, TestOutcome>>>({})
  const [testingRole, setTestingRole] = useState<Role | null>(null)
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
  const investigatorModel = data.role_models.investigator
  const staleInvestigator = investigatorModel !== undefined && !investigatorModel.available
  const reviewerModel = data.role_models.reviewer
  const staleReviewer = reviewerModel !== undefined && !reviewerModel.available
  const budgetValid = ALLOWANCE_PATTERN.test(draft.budget.trim())
  const dirty =
    draft.budget !== baseline.budget ||
    draft.provider !== baseline.provider ||
    draft.model !== baseline.model ||
    draft.investigatorProvider !== baseline.investigatorProvider ||
    draft.investigatorModel !== baseline.investigatorModel ||
    draft.reviewerProvider !== baseline.reviewerProvider ||
    draft.reviewerModel !== baseline.reviewerModel ||
    !sameList(draft.unredacted, baseline.unredacted)
  // A provider without a model is half a choice; no provider at all is fine and leaves chat out of the request.
  const modelChosen = (draft.provider === '' || draft.model !== '') && (draft.investigatorProvider === '' || draft.investigatorModel !== '') && (draft.reviewerProvider === '' || draft.reviewerModel !== '')
  const canSave = dirty && budgetValid && modelChosen && !save.isPending
  const stillStale = staleModel && draft.provider === baseline.provider && draft.model === baseline.model
  const investigatorStillStale = staleInvestigator && draft.investigatorProvider === baseline.investigatorProvider && draft.investigatorModel === baseline.investigatorModel
  const reviewerStillStale = staleReviewer && draft.reviewerProvider === baseline.reviewerProvider && draft.reviewerModel === baseline.reviewerModel

  const settled = Number(data.spend.settled_usd)
  const reserved = Number(data.spend.reserved_usd)
  const budget = Number(data.monthly_budget_usd)
  // What one question reserves is known for the saved model only; a changed allowance is judged here, an unchanged one by the server.
  const reservation = chat !== undefined && chat.max_question_usd !== '' && draft.provider === chat.provider && draft.model === chat.model ? chat.max_question_usd : ''
  // Whole cents, so an allowance that leaves exactly one question's reservation is not called too small by floating-point error.
  const cents = (dollars: string) => Math.round(Number(dollars) * 100)
  const belowQuestion =
    reservation !== '' &&
    (draft.budget !== baseline.budget && budgetValid
      ? cents(draft.budget.trim()) - cents(data.spend.settled_usd) - cents(data.spend.reserved_usd) < cents(reservation)
      : chat?.allowance_below_question === true)
  const usedPct = budget > 0 ? ((settled + reserved) / budget) * 100 : 0

  // Providers shown in the redaction section: the catalogue plus any stored one the operator no longer offers.
  const redactionProviders = [...providers.map((entry) => entry.provider), ...draft.unredacted.filter((id) => !providers.some((entry) => entry.provider === id))]

  const submit = () => {
    save.mutate(
      {
        version: data.version,
        monthly_budget_usd: draft.budget.trim(),
        unredacted_providers: draft.unredacted,
        role_models: {
          ...(draft.provider !== '' && draft.model !== '' ? { chat: { provider: draft.provider, model: draft.model } } : {}),
          // Always stated: the server keeps a stored model when the key is absent, so clearing needs an explicit null.
          investigator: draft.investigatorProvider !== '' && draft.investigatorModel !== '' ? { provider: draft.investigatorProvider, model: draft.investigatorModel } : null,
          reviewer: draft.reviewerProvider !== '' && draft.reviewerModel !== '' ? { provider: draft.reviewerProvider, model: draft.reviewerModel } : null,
        },
      },
      {
        onSuccess: () => {
          setEdited(null)
          setTestResults({})
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
              const investigatorTouched = mine.investigatorProvider !== baseline.investigatorProvider || mine.investigatorModel !== baseline.investigatorModel
              const reviewerTouched = mine.reviewerProvider !== baseline.reviewerProvider || mine.reviewerModel !== baseline.reviewerModel
              setEdited({
                budget: mine.budget !== baseline.budget ? mine.budget : stored.budget,
                provider: modelTouched ? mine.provider : stored.provider,
                model: modelTouched ? mine.model : stored.model,
                investigatorProvider: investigatorTouched ? mine.investigatorProvider : stored.investigatorProvider,
                investigatorModel: investigatorTouched ? mine.investigatorModel : stored.investigatorModel,
                reviewerProvider: reviewerTouched ? mine.reviewerProvider : stored.reviewerProvider,
                reviewerModel: reviewerTouched ? mine.reviewerModel : stored.reviewerModel,
                unredacted: mergeUnredacted(baseline.unredacted, mine.unredacted, stored.unredacted),
              })
            })
          } else {
            toast.show(error.message, 'bad')
          }
        },
      },
    )
  }

  const runTest = (role: Role) => {
    setTestResults((current) => ({ ...current, [role]: undefined }))
    setTestingRole(role)
    const tested =
      role === 'chat'
        ? { provider: draft.provider, model: draft.model }
        : role === 'investigator'
          ? { provider: draft.investigatorProvider, model: draft.investigatorModel }
          : { provider: draft.reviewerProvider, model: draft.reviewerModel }
    test.mutate(tested, {
      onSuccess: (result) => setTestResults((current) => ({ ...current, [role]: { ...result, ...tested } })),
      onError: (error) =>
        setTestResults((current) => ({
          ...current,
          [role]: { ok: false, kind: 'error', message: error instanceof ApiError ? error.message : 'The test could not run. Try again.', ...tested },
        })),
    })
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
        <>
          <ModelPanel
            title="Model for chat"
            name=""
            providers={providers}
            provider={draft.provider}
            model={draft.model}
            stale={staleModel && stillStale}
            onProvider={(provider) => patch({ provider, model: '' })}
            onModel={(model) => patch({ model })}
            result={testResults.chat ?? null}
            testing={test.isPending && testingRole === 'chat'}
            onTest={() => runTest('chat')}
          />
          <ModelPanel
            title="Model for investigations"
            name="investigation"
            providers={providers}
            provider={draft.investigatorProvider}
            model={draft.investigatorModel}
            stale={staleInvestigator && investigatorStillStale}
            onProvider={(investigatorProvider) => patch({ investigatorProvider, investigatorModel: '' })}
            onModel={(investigatorModel) => patch({ investigatorModel })}
            result={testResults.investigator ?? null}
            testing={test.isPending && testingRole === 'investigator'}
            onTest={() => runTest('investigator')}
          />
          <ModelPanel
            title="Model for reviews"
            name="review"
            providers={providers}
            provider={draft.reviewerProvider}
            model={draft.reviewerModel}
            stale={staleReviewer && reviewerStillStale}
            onProvider={(reviewerProvider) => patch({ reviewerProvider, reviewerModel: '' })}
            onModel={(reviewerModel) => patch({ reviewerModel })}
            result={testResults.reviewer ?? null}
            testing={test.isPending && testingRole === 'reviewer'}
            onTest={() => runTest('reviewer')}
          />
        </>
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
        {reservation !== '' && <p className={styles.hint}>{`One question reserves up to $${reservation} with this model.`}</p>}
        {belowQuestion && <Banner kind="warn">The allowance is below what one question reserves, so every question will be refused.</Banner>}
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

      <IdentitySection identity={data.identity} />

      <ReviewsSection settings={data} />

      <InvestigationsTable limits={data.investigator} />

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
