import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { beforeEach, describe, expect, it } from 'vitest'
import {
  ME_ADMIN,
  ME_OMER,
  SSO_CONFIGURED_OFF,
  SSO_CONFIGURED_ON,
  SSO_TEST_FAIL,
  SSO_TEST_OK,
  SSO_TEST_WOULD_LINK,
  SSO_UNCONFIGURED,
} from '../../../test/fixtures'
import type { SSOConfigDTO } from '../../../api/types'
import { installPhoneMedia } from '../../../test/phone'
import { server } from '../../../test/msw'
import { renderApp } from '../../../test/renderApp'

beforeEach(() => {
  localStorage.clear()
  window.history.replaceState(null, '', '/')
})

const GUARD_TITLE =
  'Flag at least one global admin as break-glass before turning on single sign-on. Create one with harbormaster create-admin, or flag an existing admin who has a password.'

function serveConfig(config: SSOConfigDTO) {
  server.use(http.get('/api/sso', () => HttpResponse.json(config)))
}

const REPLACE_TITLE = 'Replace the identity provider configuration?'
const REPLACE_WARNING =
  'Changing the identity provider signs out every SSO user and detaches every linked and pending SSO identity. Users keep their permissions and must be linked again, or will be created again at their next sign-in.'

describe('SSOPage replace-IdP confirm', () => {
  const countMetadataPosts = () => {
    const bodies: unknown[] = []
    server.use(
      http.post('/api/sso/metadata', async ({ request }) => {
        bodies.push(await request.json())
        return HttpResponse.json(SSO_CONFIGURED_OFF)
      }),
    )
    return bodies
  }
  const fetchByUrl = async () => {
    await userEvent.type(await screen.findByLabelText('Metadata URL'), 'https://idp.example.test/saml/metadata.xml')
    await userEvent.click(screen.getByRole('button', { name: 'Fetch' }))
  }
  const uploadFile = async () => {
    await userEvent.click(await screen.findByRole('button', { name: 'Metadata file' }))
    await userEvent.upload(screen.getByLabelText('Metadata file'), new File(['<EntityDescriptor/>'], 'idp.xml', { type: 'text/xml' }))
    await userEvent.click(screen.getByRole('button', { name: 'Upload' }))
  }

  it('Fetch with a stored entity id confirms first, then posts', async () => {
    serveConfig(SSO_CONFIGURED_OFF)
    const bodies = countMetadataPosts()
    renderApp('/settings/sso')
    await fetchByUrl()
    expect(await screen.findByText(REPLACE_TITLE)).toBeInTheDocument()
    expect(screen.getByText(REPLACE_WARNING)).toBeInTheDocument()
    expect(bodies).toHaveLength(0)
    await userEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Continue' }))
    await waitFor(() => expect(bodies).toEqual([{ url: 'https://idp.example.test/saml/metadata.xml' }]))
  })

  it('cancelling the Fetch confirm sends no request', async () => {
    serveConfig(SSO_CONFIGURED_OFF)
    const bodies = countMetadataPosts()
    renderApp('/settings/sso')
    await fetchByUrl()
    await userEvent.click(within(await screen.findByRole('dialog')).getByRole('button', { name: 'Cancel' }))
    expect(screen.queryByText(REPLACE_TITLE)).not.toBeInTheDocument()
    expect(bodies).toHaveLength(0)
  })

  it('Fetch with nothing stored posts without a confirm', async () => {
    serveConfig(SSO_UNCONFIGURED)
    const bodies = countMetadataPosts()
    renderApp('/settings/sso')
    await fetchByUrl()
    await waitFor(() => expect(bodies).toHaveLength(1))
    expect(screen.queryByText(REPLACE_TITLE)).not.toBeInTheDocument()
  })

  it('Upload with a stored entity id confirms first, then posts', async () => {
    serveConfig(SSO_CONFIGURED_OFF)
    const bodies = countMetadataPosts()
    renderApp('/settings/sso')
    await uploadFile()
    expect(await screen.findByText(REPLACE_TITLE)).toBeInTheDocument()
    expect(bodies).toHaveLength(0)
    await userEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Continue' }))
    await waitFor(() => expect(bodies).toEqual([{ xml: '<EntityDescriptor/>' }]))
  })

  it('cancelling the Upload confirm sends no request', async () => {
    serveConfig(SSO_CONFIGURED_OFF)
    const bodies = countMetadataPosts()
    renderApp('/settings/sso')
    await uploadFile()
    await userEvent.click(within(await screen.findByRole('dialog')).getByRole('button', { name: 'Cancel' }))
    expect(bodies).toHaveLength(0)
  })

  it('Refresh confirms first, then posts the stored metadata URL; cancel sends nothing', async () => {
    serveConfig(SSO_CONFIGURED_OFF)
    const bodies = countMetadataPosts()
    renderApp('/settings/sso')
    await userEvent.click(await screen.findByRole('button', { name: 'Refresh' }))
    await userEvent.click(within(await screen.findByRole('dialog')).getByRole('button', { name: 'Cancel' }))
    expect(bodies).toHaveLength(0)
    await userEvent.click(screen.getByRole('button', { name: 'Refresh' }))
    expect(await screen.findByText(REPLACE_TITLE)).toBeInTheDocument()
    await userEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Continue' }))
    await waitFor(() => expect(bodies).toEqual([{ url: SSO_CONFIGURED_OFF.metadata_url }]))
  })
})

describe('SSOPage', () => {
  it('unconfigured state shows SP values and the three import modes', async () => {
    serveConfig({ ...SSO_UNCONFIGURED, break_glass_admins: 0 })
    renderApp('/settings/sso')
    expect(await screen.findByText(SSO_UNCONFIGURED.acs_url)).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Download SP metadata' })).toHaveAttribute('href', '/api/auth/saml/metadata')
    for (const label of ['Metadata URL', 'Metadata file', 'Manual']) {
      expect(screen.getByRole('button', { name: label })).toBeInTheDocument()
    }
    const toggle = screen.getByRole('button', { name: 'Turn on single sign-on' })
    expect(toggle).toBeDisabled()
    expect(toggle).toHaveAttribute('title', GUARD_TITLE)
    expect(screen.getByText('SSO off')).toBeInTheDocument()
  })

  it('fetching metadata by URL posts {url} and shows the IdP summary', async () => {
    serveConfig(SSO_UNCONFIGURED)
    let body: unknown
    server.use(
      http.post('/api/sso/metadata', async ({ request }) => {
        body = await request.json()
        serveConfig(SSO_CONFIGURED_OFF)
        return HttpResponse.json(SSO_CONFIGURED_OFF)
      }),
    )
    renderApp('/settings/sso')
    await userEvent.type(await screen.findByLabelText('Metadata URL'), 'https://idp.example.test/saml/metadata.xml')
    await userEvent.click(screen.getByRole('button', { name: 'Fetch' }))
    expect(await screen.findByText(SSO_CONFIGURED_OFF.idp_certs[0].fingerprint)).toBeInTheDocument()
    expect(body).toEqual({ url: 'https://idp.example.test/saml/metadata.xml' })
  })

  it('uploading a metadata file posts {xml}', async () => {
    serveConfig(SSO_UNCONFIGURED)
    let body: unknown
    server.use(
      http.post('/api/sso/metadata', async ({ request }) => {
        body = await request.json()
        return HttpResponse.json(SSO_CONFIGURED_OFF)
      }),
    )
    renderApp('/settings/sso')
    await userEvent.click(await screen.findByRole('button', { name: 'Metadata file' }))
    await userEvent.upload(screen.getByLabelText('Metadata file'), new File(['<EntityDescriptor/>'], 'idp.xml', { type: 'text/xml' }))
    await userEvent.click(screen.getByRole('button', { name: 'Upload' }))
    await waitFor(() => expect(body).toEqual({ xml: '<EntityDescriptor/>' }))
  })

  it('shows the API message when the metadata import is rejected', async () => {
    serveConfig(SSO_UNCONFIGURED)
    server.use(
      http.post('/api/sso/metadata', () =>
        HttpResponse.json({ error: { code: 'invalid', message: 'Metadata has no IDPSSODescriptor.' } }, { status: 422 }),
      ),
    )
    renderApp('/settings/sso')
    await userEvent.type(await screen.findByLabelText('Metadata URL'), 'https://idp.example.test/m')
    await userEvent.click(screen.getByRole('button', { name: 'Fetch' }))
    expect(await screen.findByText('Metadata has no IDPSSODescriptor.')).toBeInTheDocument()
  })

  it('manual save sends PUT /api/sso with the typed fields', async () => {
    serveConfig(SSO_UNCONFIGURED)
    let body: Record<string, unknown> | undefined
    server.use(
      http.put('/api/sso', async ({ request }) => {
        body = (await request.json()) as Record<string, unknown>
        return HttpResponse.json(SSO_CONFIGURED_OFF)
      }),
    )
    renderApp('/settings/sso')
    await userEvent.click(await screen.findByRole('button', { name: 'Manual' }))
    await userEvent.type(screen.getByLabelText('Entity ID'), 'https://idp.example.test/saml')
    await userEvent.type(screen.getByLabelText('SSO URL (HTTP-Redirect)'), 'https://idp.example.test/saml/sso')
    const pem = '-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----'
    await userEvent.click(screen.getByLabelText('Certificate PEM'))
    await userEvent.paste(pem)
    await userEvent.click(screen.getByRole('button', { name: 'Save identity provider' }))
    await waitFor(() => expect(body).toBeDefined())
    expect(body).toMatchObject({
      idp_entity_id: 'https://idp.example.test/saml',
      idp_sso_redirect_url: 'https://idp.example.test/saml/sso',
      idp_certs: [pem],
    })
  })

  it('manual save omits idp_certs when no PEM is typed so stored certificates are kept', async () => {
    serveConfig(SSO_CONFIGURED_OFF)
    let body: Record<string, unknown> | undefined
    server.use(
      http.put('/api/sso', async ({ request }) => {
        body = (await request.json()) as Record<string, unknown>
        return HttpResponse.json(SSO_CONFIGURED_OFF)
      }),
    )
    renderApp('/settings/sso')
    await userEvent.click(await screen.findByRole('button', { name: 'Manual' }))
    await userEvent.click(screen.getByRole('button', { name: 'Save identity provider' }))
    await waitFor(() => expect(body).toBeDefined())
    expect(body).not.toHaveProperty('idp_certs')
  })

  it('entity id change shows the warning', async () => {
    serveConfig(SSO_CONFIGURED_OFF)
    renderApp('/settings/sso')
    await userEvent.click(await screen.findByRole('button', { name: 'Manual' }))
    const warning =
      'Changing the identity provider signs out every SSO user and detaches every linked and pending SSO identity. Users keep their permissions and must be linked again, or will be created again at their next sign-in.'
    expect(screen.queryByText(warning)).not.toBeInTheDocument()
    await userEvent.type(screen.getByLabelText('Entity ID'), '-new')
    expect(screen.getByText(warning)).toBeInTheDocument()
  })

  it('enable confirms with the spec copy including the blocked-users count and sends enabled:true', async () => {
    serveConfig(SSO_CONFIGURED_OFF)
    let body: unknown
    server.use(
      http.put('/api/sso/enabled', async ({ request }) => {
        body = await request.json()
        return HttpResponse.json(SSO_CONFIGURED_ON)
      }),
    )
    renderApp('/settings/sso')
    await userEvent.click(await screen.findByRole('button', { name: 'Turn on single sign-on' }))
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText('Turn on single sign-on?')).toBeInTheDocument()
    expect(
      within(dialog).getByText(
        '2 users have a password but are not break-glass; they can only sign in with SSO while it is on. Passwords are kept and work again if you turn it off.',
      ),
    ).toBeInTheDocument()
    await userEvent.click(within(dialog).getByRole('button', { name: 'Turn on' }))
    await waitFor(() => expect(body).toEqual({ enabled: true }))
  })

  it('enable confirm uses the zero-case sentence when no password user is blocked', async () => {
    serveConfig({ ...SSO_CONFIGURED_OFF, password_users_blocked_on_enable: 0 })
    renderApp('/settings/sso')
    await userEvent.click(await screen.findByRole('button', { name: 'Turn on single sign-on' }))
    expect(
      await screen.findByText('Only break-glass accounts can sign in with a password while single sign-on is on.'),
    ).toBeInTheDocument()
  })

  it('shows the guard message from the server when enabling is refused', async () => {
    serveConfig(SSO_CONFIGURED_OFF)
    server.use(
      http.put('/api/sso/enabled', () =>
        HttpResponse.json(
          { error: { code: 'invalid', message: 'Flag at least one global admin as break-glass before turning on single sign-on.' } },
          { status: 422 },
        ),
      ),
    )
    renderApp('/settings/sso')
    await userEvent.click(await screen.findByRole('button', { name: 'Turn on single sign-on' }))
    await userEvent.click(within(await screen.findByRole('dialog')).getByRole('button', { name: 'Turn on' }))
    expect(
      await screen.findByText('Flag at least one global admin as break-glass before turning on single sign-on.'),
    ).toBeInTheDocument()
  })

  it('disable confirms with the spec copy and sends enabled:false', async () => {
    serveConfig(SSO_CONFIGURED_ON)
    let body: unknown
    server.use(
      http.put('/api/sso/enabled', async ({ request }) => {
        body = await request.json()
        return HttpResponse.json(SSO_CONFIGURED_OFF)
      }),
    )
    renderApp('/settings/sso')
    expect(await screen.findByText('SSO on')).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'Turn off single sign-on' }))
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText('Turn off single sign-on?')).toBeInTheDocument()
    expect(
      within(dialog).getByText(
        'Everyone signed in with SSO is signed out now. Users with a password can sign in with it again; users without one need an admin to set one.',
      ),
    ).toBeInTheDocument()
    await userEvent.click(within(dialog).getByRole('button', { name: 'Turn off' }))
    await waitFor(() => expect(body).toEqual({ enabled: false }))
  })

  it('shows the link-your-own-account hint when me is not linked', async () => {
    serveConfig(SSO_CONFIGURED_OFF)
    renderApp('/settings/sso')
    expect(
      await screen.findByText('Link your own account first so you can sign in with SSO: use Link SSO account in the sidebar.'),
    ).toBeInTheDocument()
  })

  it('on a phone the hint points to More, and the guard reason is visible text', async () => {
    const restore = installPhoneMedia(true)
    try {
      serveConfig({ ...SSO_UNCONFIGURED, break_glass_admins: 0 })
      renderApp('/settings/sso')
      expect(await screen.findByText('Link your own account first so you can sign in with SSO: use Link SSO account in More.')).toBeInTheDocument()
      expect(screen.getByRole('button', { name: 'Turn on single sign-on' })).toBeDisabled()
      expect(screen.getByText(GUARD_TITLE)).toBeVisible()
    } finally {
      restore()
    }
  })

  it('hides the hint when the admin is already linked', async () => {
    serveConfig(SSO_CONFIGURED_OFF)
    server.use(http.get('/api/me', () => HttpResponse.json({ ...ME_ADMIN, sso_linked: true })))
    renderApp('/settings/sso')
    expect(await screen.findByText('SSO off')).toBeInTheDocument()
    expect(screen.queryByText(/Link your own account first/)).not.toBeInTheDocument()
  })

  it('renders warnings in a banner', async () => {
    serveConfig({ ...SSO_CONFIGURED_OFF, warnings: ['Certificate CN=idp expires in 12 days.'] })
    renderApp('/settings/sso')
    expect(await screen.findByText('Certificate CN=idp expires in 12 days.')).toBeInTheDocument()
  })

  it('rotating the SP certificate confirms with the spec copy and posts /api/sso/key', async () => {
    serveConfig(SSO_CONFIGURED_OFF)
    let called = false
    server.use(
      http.post('/api/sso/key', () => {
        called = true
        return HttpResponse.json(SSO_CONFIGURED_OFF)
      }),
    )
    renderApp('/settings/sso')
    await userEvent.click(await screen.findByRole('button', { name: 'Generate new certificate' }))
    const dialog = await screen.findByRole('dialog')
    expect(
      within(dialog).getByText(
        'The current certificate keeps decrypting assertions for 7 days. Update your identity provider with the new SP metadata.',
      ),
    ).toBeInTheDocument()
    await userEvent.click(within(dialog).getByRole('button', { name: 'Generate new certificate' }))
    await waitFor(() => expect(called).toBe(true))
  })

  it('saving sign-in options sends PUT /api/sso with the typed values', async () => {
    serveConfig(SSO_CONFIGURED_OFF)
    let body: Record<string, unknown> | undefined
    server.use(
      http.put('/api/sso', async ({ request }) => {
        body = (await request.json()) as Record<string, unknown>
        return HttpResponse.json(SSO_CONFIGURED_OFF)
      }),
    )
    renderApp('/settings/sso')
    const label = await screen.findByLabelText('Button label')
    await userEvent.clear(label)
    await userEvent.type(label, 'Sign in with Acme')
    await userEvent.selectOptions(screen.getByLabelText('NameID format'), 'email')
    expect(screen.getByLabelText('Username attribute')).toHaveAttribute('placeholder', 'Derived from email')
    await userEvent.type(screen.getByLabelText('Username attribute'), 'uid')
    const skew = screen.getByLabelText('Clock skew (seconds)')
    await userEvent.clear(skew)
    await userEvent.type(skew, '30')
    await userEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(body).toBeDefined())
    expect(body).toMatchObject({
      button_label: 'Sign in with Acme',
      name_id_format: 'email',
      attr_email: 'email',
      attr_display_name: 'displayName',
      attr_username: 'uid',
      clock_skew_seconds: 30,
      idp_entity_id: SSO_CONFIGURED_OFF.idp_entity_id,
    })
    expect(body).not.toHaveProperty('idp_certs')
  })

  it('offers the three NameID formats', async () => {
    serveConfig(SSO_CONFIGURED_OFF)
    renderApp('/settings/sso')
    const select = await screen.findByLabelText('NameID format')
    expect(within(select).getAllByRole('option').map((option) => option.textContent)).toEqual([
      'Let the IdP choose',
      'Email address',
      'Persistent',
    ])
  })

  it('Test sign-in opens the IdP flow in a new tab', async () => {
    serveConfig(SSO_CONFIGURED_OFF)
    renderApp('/settings/sso')
    const link = within(await screen.findByRole('region', { name: 'Test sign-in' })).getByRole('link', { name: 'Test sign-in' })
    expect(link).toHaveAttribute('href', '/api/auth/saml/login?test=1')
    expect(link).toHaveAttribute('target', '_blank')
  })

  it('test result ok renders Would create user alex and the copyable NameID', async () => {
    serveConfig(SSO_CONFIGURED_OFF)
    server.use(http.get('/api/sso/tests/:id', () => HttpResponse.json({ ...SSO_TEST_OK, username: 'alex' })))
    renderApp('/settings/sso?test=abc')
    expect(await screen.findByText('Sign-in test passed.')).toBeInTheDocument()
    expect(screen.getByText('Would create user alex')).toBeInTheDocument()
    expect(screen.getByDisplayValue(SSO_TEST_OK.name_id)).toHaveAttribute('readonly')
    expect(screen.getByText('Use this value for pending links.')).toBeInTheDocument()
    expect(screen.getByText('displayName')).toBeInTheDocument()
  })

  it('test result would_sign_in renders Would sign in', async () => {
    serveConfig(SSO_CONFIGURED_OFF)
    server.use(
      http.get('/api/sso/tests/:id', () =>
        HttpResponse.json({ ...SSO_TEST_OK, outcome: 'would_sign_in', username: 'sam', existing_user: 'sam' }),
      ),
    )
    renderApp('/settings/sso?test=abc')
    expect(await screen.findByText('Would sign in sam')).toBeInTheDocument()
  })

  it('test result would_link renders Would link dana (pending link)', async () => {
    serveConfig(SSO_CONFIGURED_OFF)
    server.use(http.get('/api/sso/tests/:id', () => HttpResponse.json(SSO_TEST_WOULD_LINK)))
    renderApp('/settings/sso?test=abc')
    expect(await screen.findByText('Would link dana (pending link)')).toBeInTheDocument()
  })

  it('test result failure renders the reason', async () => {
    serveConfig(SSO_CONFIGURED_OFF)
    server.use(http.get('/api/sso/tests/:id', () => HttpResponse.json(SSO_TEST_FAIL)))
    renderApp('/settings/sso?test=abc')
    expect(await screen.findByText(`Sign-in test failed: ${SSO_TEST_FAIL.message}`)).toBeInTheDocument()
    expect(screen.getByText('audience')).toBeInTheDocument()
  })

  it('Dismiss removes the test parameter', async () => {
    serveConfig(SSO_CONFIGURED_OFF)
    renderApp('/settings/sso?test=abc')
    await userEvent.click(await screen.findByRole('button', { name: 'Dismiss' }))
    expect(screen.queryByText('Sign-in test passed.')).not.toBeInTheDocument()
    expect(screen.getByTestId('search')).toHaveTextContent('')
  })

  it('non-admin sees AdminOnly copy', async () => {
    server.use(http.get('/api/me', () => HttpResponse.json(ME_OMER)))
    renderApp('/settings/sso')
    expect(await screen.findByText('Only global admins change single sign-on.')).toBeInTheDocument()
  })
})

describe('SSOPage setup checklist', () => {
  const stepStates = async () => {
    const list = await screen.findByRole('list', { name: 'Set up single sign-on' })
    return within(list)
      .getAllByRole('listitem')
      .map((item) => [within(item).getByRole('link').textContent, item.getAttribute('data-state')])
  }

  it('steps derive from config', async () => {
    serveConfig(SSO_UNCONFIGURED)
    const { unmount } = renderApp('/settings/sso')
    expect(await stepStates()).toEqual([
      ['Identity provider', 'todo'],
      ['Service provider', 'blocked'],
      ['Sign-in options', 'blocked'],
      ['Test sign-in', 'blocked'],
      ['Turn on', 'blocked'],
    ])
    unmount()

    serveConfig(SSO_CONFIGURED_OFF)
    const second = renderApp('/settings/sso')
    expect(await stepStates()).toEqual([
      ['Identity provider', 'done'],
      ['Service provider', 'done'],
      ['Sign-in options', 'done'],
      ['Test sign-in', 'todo'],
      ['Turn on', 'todo'],
    ])
    second.unmount()

    serveConfig(SSO_CONFIGURED_ON)
    renderApp('/settings/sso')
    expect((await stepStates()).map(([, state]) => state)).toEqual(['done', 'done', 'done', 'done', 'done'])
  })

  it('links each step to its panel by anchor', async () => {
    serveConfig(SSO_CONFIGURED_OFF)
    renderApp('/settings/sso')
    const list = await screen.findByRole('list', { name: 'Set up single sign-on' })
    const hrefs = within(list).getAllByRole('link').map((link) => link.getAttribute('href'))
    expect(hrefs).toEqual(['#sso-idp', '#sso-sp', '#sso-options', '#sso-test', '#sso-on'])
    for (const href of hrefs) expect(document.querySelector(href!)).not.toBeNull()
  })

  it('Turn on blocked without break-glass keeps the tooltip', async () => {
    serveConfig({ ...SSO_CONFIGURED_OFF, break_glass_admins: 0 })
    renderApp('/settings/sso')
    const states = await stepStates()
    expect(states[4]).toEqual(['Turn on', 'blocked'])
    const button = screen.getByRole('button', { name: 'Turn on single sign-on' })
    expect(button).toBeDisabled()
    expect(button).toHaveAttribute('title', GUARD_TITLE)
  })
})
