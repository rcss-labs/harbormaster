import type { SSOConfigDTO } from '../../../api/types'
import { Panel } from '../../../components/Panel'
import { Pill } from '../../../components/Pill'
import styles from './sso.module.css'

export type StepState = 'done' | 'todo' | 'blocked'

export interface SetupStep {
  label: string
  href: string
  state: StepState
  hint: string
}

/** The five setup steps, each derived from the saved configuration only (a test result is never stored). */
export function setupSteps(config: SSOConfigDTO): SetupStep[] {
  const configured = config.configured
  const optionsCustomised =
    config.name_id_format !== 'unspecified' || config.attr_email !== null || config.attr_display_name !== null || config.attr_username !== null
  const blockedUnless = (ready: boolean, done: boolean): StepState => (!ready ? 'blocked' : done ? 'done' : 'todo')
  const noBreakGlass = config.break_glass_admins === 0
  return [
    {
      label: 'Identity provider',
      href: '#sso-idp',
      state: configured ? 'done' : 'todo',
      hint: configured ? 'Metadata saved' : 'Fetch metadata or enter it by hand',
    },
    {
      label: 'Service provider',
      href: '#sso-sp',
      state: blockedUnless(configured, config.sp_cert !== null && config.sp_key_state === 'ok'),
      hint: !configured ? 'Created when you save the identity provider' : config.sp_key_state === 'ok' ? 'Certificate created' : 'Check the certificate',
    },
    {
      label: 'Sign-in options',
      href: '#sso-options',
      state: blockedUnless(configured, optionsCustomised),
      hint: !configured ? 'Save the identity provider first' : optionsCustomised ? 'Saved' : 'Button label and attribute names',
    },
    {
      label: 'Test sign-in',
      href: '#sso-test',
      state: config.enabled ? 'done' : blockedUnless(configured, false),
      hint: !configured ? 'Save the identity provider first' : 'Check what a sign-in would do',
    },
    {
      label: 'Turn on',
      href: '#sso-on',
      state: config.enabled ? 'done' : !configured || noBreakGlass ? 'blocked' : 'todo',
      hint: config.enabled ? 'Single sign-on is on' : !configured ? 'Save the identity provider first' : noBreakGlass ? 'Needs a break-glass admin' : 'Ready to turn on',
    },
  ]
}

const LABEL: Record<StepState, string> = { done: 'Done', todo: 'To do', blocked: 'Blocked' }
const KIND = { done: 'ok', todo: 'info', blocked: 'mute' } as const

export function SetupChecklist({ config }: { config: SSOConfigDTO }) {
  const steps = setupSteps(config)
  const doneCount = steps.filter((step) => step.state === 'done').length
  return (
    <Panel
      title="Set up single sign-on"
      actions={
        <span>
          {doneCount} of {steps.length} done
        </span>
      }
    >
      <ol className={styles.steps} aria-label="Set up single sign-on">
        {steps.map((step, index) => (
          <li key={step.label} data-state={step.state} className={styles.step}>
            <span className={styles.stepNo} aria-hidden="true">
              {index + 1}
            </span>
            <span className={styles.stepText}>
              <a href={step.href} className={styles.stepLink}>
                {step.label}
              </a>
              <span className={styles.hint}>{step.hint}</span>
            </span>
            <Pill kind={KIND[step.state]}>{LABEL[step.state]}</Pill>
          </li>
        ))}
      </ol>
    </Panel>
  )
}
