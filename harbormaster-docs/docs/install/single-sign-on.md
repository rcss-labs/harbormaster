# Single sign-on

This page shows how to sign users in through a SAML 2.0 identity provider (IdP) such as Okta, Entra ID, Google Workspace or Keycloak. It is for global admins and the IdP owner.

SSO is off until a global admin turns it on. Once it is on, SSO is the normal way in, and only break-glass accounts can also sign in with a password.

## Before you begin

- `HM_SESSION_KEY` must be set. The `harbormaster` module sets it. Harbormaster encrypts its SSO signing key under it, and saving the SSO configuration fails with "Set HM_SESSION_KEY before configuring single sign-on." without it.
- `HM_BASE_URL` (the module's `base_url`) must be the URL users open in their browser. The SP entity ID, the ACS URL and the destination check of every sign-in come from it.
- At least one global admin must be a break-glass admin before SSO can be turned on.

The module has no SSO inputs. You configure SSO in the web app after the module is applied.

Sign-in is browser-mediated: the IdP posts its response through the user's browser to `{base_url}/api/auth/saml/acs`. The IdP never calls the load balancer directly, so an internal ALB works. `allowed_cidrs` only has to admit your users' networks.

The one call the task makes itself is fetching a metadata URL. The IdP must have a public `https` address for that, or you upload its metadata XML instead.

## Set up the identity provider

Replace `{base_url}` with your `HM_BASE_URL`, for example `https://harbormaster.example.com`.

| Value | URL |
| --- | --- |
| ACS URL (POST binding) | `{base_url}/api/auth/saml/acs` |
| SP entity ID (audience) | `{base_url}/api/auth/saml/metadata` (the default; Settings shows the value in use) |
| App login URL | `{base_url}/api/auth/saml/login` |

The SP metadata is at `{base_url}/api/auth/saml/metadata`, and Settings has a "Download SP metadata" link. Upload it to the IdP if the IdP asks for a metadata file.

Follow your IdP's standard SAML app setup. These notes name only the fields that matter for Harbormaster.

- **Okta:** set the ACS URL and the audience to the values above. Set "Login initiated by" to "Either Okta or App" and the App login URL to `{base_url}/api/auth/saml/login`, so the Okta tile starts sign-in at Harbormaster.
- **Entra ID:** set the identifier to the SP entity ID, the reply URL to the ACS URL, and the **Sign on URL** to `{base_url}/api/auth/saml/login`. The My Apps tile opens the Sign on URL.
- **Google Workspace:** set the ACS URL and entity ID to the values above, and set the **Start URL** to `{base_url}/api/auth/saml/login`.
- **Keycloak:** set the ACS URL and entity ID to the values above. Set the client's **Home URL** to `{base_url}/api/auth/saml/login`. Turn **Client signature required** off, because Harbormaster does not sign its sign-in requests. Do not add an **Audience** protocol mapper: Harbormaster reads one `Audience` per audience restriction, so extra audiences make sign-in fail with reason `audience`.

## Give Harbormaster the IdP settings

Open Settings > Single sign-on > Identity provider and use one of:

- **Metadata URL** or **Metadata file**: the simplest choice. A metadata URL must be `https` and must not point at a loopback, link-local or private address. For an IdP on a private network, such as an internal Keycloak, upload the metadata XML file.
- **Manual**: the entity ID, the SSO URL (HTTP-Redirect binding) and one or more signing certificates in PEM form.

Harbormaster accepts an assertion only when one of the configured certificates signed it.

### NameID

Under Settings > Single sign-on > Sign-in options, the default NameID format is "Let the IdP choose" (`unspecified`). "Email address" and "Persistent" also work. Transient NameIDs are rejected, and Test sign-in shows the error.

Harbormaster identifies a user by the IdP entity ID and the NameID, not by email. The NameID must stay the same for each person.

## Turn it on

Follow this order. Each step is a safe stop, and nobody is locked out before SSO is enabled.

1. **Configure.** Save the identity provider settings and sign-in options, and set the app values in your IdP.
2. **Test.** Use Settings > Single sign-on > Test sign-in. It shows the NameID, its format, the attributes it received and the username Harbormaster would create. It creates no user and no session, and it works while SSO is off.
3. **Link your own account.** Open "Link SSO account" in the sidebar, enter your current password, and sign in at the IdP as yourself.
4. **Flag a break-glass admin.** On the Users page, make at least one admin break-glass, so you can still sign in with a password if the IdP fails.
5. **Set pending links.** For existing local users who should sign in with SSO, set a pending link to the NameID that Test sign-in showed.
6. **Enable.** Turn on single sign-on. The confirm dialog says how many users with a password are not break-glass and so can no longer sign in with one.

## Linking users

- **First SSO sign-in.** When no link exists, the sign-in creates a new user with no permissions, not admin. The username comes from the IdP attributes. Harbormaster never attaches an SSO sign-in to an existing user by username or email. A global admin grants access in Users and permissions.
- **Self-link.** A signed-in user with a password opens "Link SSO account", enters their current password, and signs in at the IdP. This works once SSO is configured, even before it is enabled.
- **Pending link.** A global admin sets a pending link on a local user on the Users page, with the exact NameID the IdP issues. The user's first SSO sign-in with that NameID uses the existing account and keeps its permissions. A pending link expires after 14 days. Set it again if needed.
- **Unlink.** A global admin can unlink a user on the Users page. The user's SSO sessions end. If the user has no password, Harbormaster sets a temporary password and shows it once; the user must change it at the next sign-in. Users cannot unlink themselves.

## Passwords

Passwords are never cleared. Linking, enabling SSO and using a pending link leave a user's password as it is.

- **While SSO is on,** only break-glass accounts can sign in with a password. Other users who enter their password get "Use single sign-on to sign in to this account."
- **While SSO is off,** everyone who still has a password can sign in with it again.
- **Users without a password** (for example SSO users created at first sign-in) cannot use a password until an admin sets one with "Set password" on the Users page.

## Break-glass and recovery

A break-glass account is a local admin who can also sign in with a password while SSO is on. Break-glass requires a password, so an SSO-only user can be made break-glass only after an admin sets a password for them. Keep only a few break-glass accounts.

- Enabling SSO requires at least one global admin who is break-glass. While SSO is on, the last break-glass admin cannot be removed, demoted or deleted.
- If the IdP is down or misconfigured, a break-glass admin signs in with the password. The login page offers "Sign in with a password instead" when SSO is on.
- If no break-glass admin can sign in, run `harbormaster create-admin --username <name>` with the same database settings as the server. It always creates a break-glass admin. Add `--password-stdin` to pass the password on standard input.

## Changing the setup

- **Changing the IdP entity ID** signs out every SSO user and detaches every linked identity of users who have a password. Pending links are cleared. Users without a password keep their old identity, which no longer matches any sign-in. An admin unlinks them (which sets a temporary password) and links them again. Users keep their permissions.
- **Turning SSO off** signs out every SSO user and restores password sign-in for everyone who has a password.
- **Removing an IdP signing certificate** signs out SSO users. Adding one does not.
- **Changing `base_url`** changes the SP entity ID and the ACS URL. Update the IdP and upload the new SP metadata.
- **Rotating `HM_SESSION_KEY`** makes the stored SP key impossible to decrypt. Settings then shows that the SP key cannot be decrypted. Click "Generate new certificate" in Settings and upload the new SP metadata to the IdP. The old certificate keeps decrypting assertions for 7 days after a new one is generated.

## Test sign-in

Settings > Single sign-on > "Test sign-in" opens a sign-in in a new tab and shows the result in Settings. It runs the same checks as a real sign-in. It is the first place to look when a sign-in fails: it shows the NameID to use for pending links, the attributes received, and the reason code for any failure.

## Limitations

- **No Single Logout.** "Sign out" ends the Harbormaster session only. Sign out at the IdP separately.
- **No IdP-initiated sign-in.** Sign-in must start from Harbormaster. Use the login URLs above for app tiles and launchers.
- **No group mapping.** Permissions are granted per user in Users and permissions.
- **Sign-in time limit.** A sign-in must complete within `HM_SAML_REQUEST_TTL` (10 minutes by default). If it takes longer, start again.

## Check it worked

- Test sign-in succeeds and shows the expected NameID.
- A user signs in from the IdP tile and lands in Harbormaster.
- A break-glass admin can still sign in with a password.

## Next steps

- [Users, tags and permissions](../guides/users-tags-permissions.md)
- [Security model](../security/security-model.md)
