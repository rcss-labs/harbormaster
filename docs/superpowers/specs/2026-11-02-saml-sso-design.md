# SAML 2.0 single sign-on with break-glass accounts

Date: 2026-11-02. Source: product owner's request to add enterprise SSO to Harbormaster (design doc §10 said "username + password only", SSO "later").

> **Decision tooling:** CLAUDE.md requires the `typesafe-ai` decision skill for design choices and library selection. The skill was **unavailable in the session that produced this spec**; every decision below was made by reading the code, the libraries and their security history without it. Where a decision is close, the alternatives and the reason for the pick are written out so the owner can overrule.

## Why

Harbormaster holds deploy and exec rights into every connected account. Today each operator has a local username and password, which means a second credential to rotate, no MFA unless the operator's browser adds it, and offboarding that depends on someone remembering to delete the Harbormaster user. Enterprise identity providers (Okta, Entra ID, Google Workspace, Keycloak) already solve those; Harbormaster should delegate sign-in to them while keeping a small, auditable way back in when the IdP is unavailable or misconfigured.

## Decisions already made by the owner (binding, not re-litigated here)

1. **SSO + break-glass.** Once SAML is enabled, SSO is the normal sign-in. Global admins flag a few local accounts as *break-glass*; only those may sign in with a password while SSO is on. Everyone else must use SSO. With SSO not configured, behaviour is exactly today's.
2. **JIT provisioning, manual permissions.** The first successful SSO sign-in creates the user with no tag permissions and not admin. A global admin grants access in the existing Users and permissions UI. No group or attribute to permission mapping. Profile attributes (email, display name) are synced on every sign-in.
3. **Configuration in the admin UI and the database.** A global admin configures the IdP in Settings: metadata XML upload or metadata URL, and/or manual entity ID, SSO URL and certificate; downloads SP metadata; enables and disables SSO. No redeploy.
4. **Single IdP.** Okta, Entra ID, Google Workspace and Keycloak must work with their standard SAML app settings.
5. **Linking existing local users is in scope** (review of 2026-11-02): the current admin must be able to link their account now, not in a follow-up. Linking is always an explicit, proven action (self-link with the password re-entered, or an admin-set pending link consumed by a matching SSO sign-in); there is still **no automatic linking by username or email**.
6. Review answers: default label "Sign in with SSO"; `create-admin` always creates break-glass admins; default NameID policy `unspecified`; IdP-initiated stays out.
7. **Passwords are never cleared** (second review): linking, enabling SSO and pending-link consumption leave `password_hash` untouched. While SSO is on, a non-break-glass password sign-in is refused with `sso_required`; turning SSO off restores password sign-in for everyone who still has one. The owner accepts the trade-off that dormant password hashes remain in the database for linked users.
8. **Pending links expire after 14 days** (second review).

## Goals

1. An operator clicks "Sign in with SSO", authenticates at the IdP, and lands back on the page they wanted, signed in to Harbormaster with the existing session machinery.
2. A global admin can set SSO up end to end from the Settings page, test it without locking anyone out, and turn it on or off.
3. Every ECS task of Harbormaster can serve any step of the flow: no ALB stickiness, no in-memory state that matters.
4. Every accepted assertion is signed by the configured IdP, addressed to this Harbormaster, answers a request this Harbormaster issued in the last ten minutes, is inside its validity window, and is accepted exactly once.
5. Recovery always exists: at least one break-glass admin while SSO is on, and `harbormaster create-admin` as the last resort.
6. The whole flow is testable locally and in CI with an in-repo fake IdP; no real IdP in tests.
7. Existing local users, starting with the admin who sets SSO up, can attach their IdP identity to the account they already have, keeping their permissions, by proving control of both credentials (D11).

## Non-goals

- Single Logout (SLO). Signing out ends the Harbormaster session only (see Decision D2).
- IdP-initiated sign-in (unsolicited responses). See Decision D1 for why and for the IdP-side setting that gives the same "click the app tile" experience.
- Group or role mapping from IdP attributes to Harbormaster permissions or admin status.
- Multiple IdPs, per-user IdP selection, or OIDC.
- SCIM provisioning or deprovisioning.
- Signing AuthnRequests (D5). The SP key pair exists for encrypted assertions and metadata.
- Automatic periodic refresh of IdP metadata from the URL (manual "Refresh" in v1; noted as follow-up).

## Anti-goals (a failure even if it technically works)

- An assertion signed by someone other than the configured IdP, or by the right IdP for another service, creates a Harbormaster session.
- Replaying a captured ACS POST creates a second session.
- An admin turns SSO on, nobody can sign in, and the only fix is a database edit.
- An SSO user silently inherits a local user's permissions because the username or email matched.
- A link attaches an attacker's IdP identity to someone else's account, or a stale pending link binds a recycled NameID to the wrong person.
- Linking or unlinking leaves a user with no working credential and no admin-visible way back.
- A `RelayState` or `return_to` value sends the browser to another origin.
- Enabling SSO changes anything for an installation that never configures it.

---

## Library evaluation

Requirements: SP side only; HTTP-Redirect binding for the AuthnRequest and HTTP-POST binding for the Response; IdP metadata parsing; SP metadata generation; XML-DSig verification resistant to signature wrapping; encrypted assertions; MIT-compatible licence; small, auditable dependency tree; maintained.

Verified on 2026-11-02 with network access: `go list -m -versions`, `go mod download -json`, each module's `go.mod`, the Go vulnerability database index (`vuln.go.dev/index/modules.json` plus each `ID/GO-….json`), the GitHub REST API (`pushed_at`, `archived`, `open_issues_count`, licence, tag commit dates), and a scratch module that built `github.com/crewjam/saml v0.5.1` together with `goxmldsig v1.6.1`.

| | `github.com/crewjam/saml` | `github.com/russellhaering/gosaml2` (+ `goxmldsig`) | Hand-rolled on `goxmldsig` |
|---|---|---|---|
| Latest | v0.5.1, tagged 2025-04-14 (v0.5.0 2025-04-12, v0.4.14 2023-10-14). Repo `pushed_at` 2026-01-29, not archived, 91 open issues, 1.1k stars | v0.12.0, 2026-08-05 (v0.11.0 2026-03-18, v0.10.0 2025-03-20). `pushed_at` 2026-09-02, 67 open issues. `goxmldsig` v1.6.1, 2026-08-04, `pushed_at` 2026-09-28 | n/a |
| Licence | BSD-2-Clause | Apache-2.0 (both) | Apache-2.0 (goxmldsig) |
| MIT-compatible | Yes (notice in `THIRD_PARTY_NOTICES.md`) | Yes (notice + NOTICE text, as already done for session-manager-plugin) | Yes |
| Direct deps (`go.mod`) | `beevik/etree v1.5.0`, `golang-jwt/jwt/v4` (only used by the `samlsp` middleware), `google/go-cmp`, `mattermost/xml-roundtrip-validator`, `russellhaering/goxmldsig v1.4.0`, `golang.org/x/crypto`; indirect `jonboulle/clockwork`, `pkg/errors` | `beevik/etree v1.7.0`, `jonboulle/clockwork`, `mattermost/xml-roundtrip-validator`, `goxmldsig v1.6.1`, `testify` | `goxmldsig`, `etree`, `clockwork`; everything else ours |
| Security history (Go vuln DB) | GO-2021-0058 / CVE-2020-27846 signature bypass via XML round-trip (fixed 0.4.3); GO-2022-1129 / CVE-2022-41912 auth bypass with multiple `Assertion` elements (fixed 0.4.9); GO-2023-1664 / CVE-2023-28119 deflate bomb (fixed 0.4.13); GO-2023-2114 / CVE-2023-45683 XSS via unvalidated binding (fixed 0.4.14). None open against v0.5.1 | gosaml2: GO-2020-0046 panic on malformed signature (fixed 0.7.0); GO-2021-0060 / CVE-2020-29509 auth bypass (fixed 0.6.0); GO-2023-1602 deflate bomb (fixed 0.9.0); GO-2026-4760 CBC padding panic (fixed 0.11.0); GO-2026-4764 unsigned LogoutRequest accepted (fixed 0.11.0). goxmldsig: GO-2020-0050 / CVE-2020-15216 validation bypass (fixed 1.1.0); GO-2020-0046 (fixed 1.1.1); **GO-2026-4753 / CVE-2026-33487 loop-variable-capture signature bypass (fixed 1.6.0)**. None open against the latest versions | Inherits goxmldsig's; adds our own untested parsing |
| Signature wrapping defence | Uses `goxmldsig`'s "validate and return the validated element" model; rejects more than one `Assertion`; XML round-trip validator rejects documents Go's parser would re-interpret | Same `goxmldsig` model; round-trip validator | Must be re-implemented correctly by us: this is exactly where the published bugs were |
| Response validation built in | `ServiceProvider.ParseXMLResponse(xml, possibleRequestIDs, currentURL)`: response **or** assertion signature required, Issuer, `Destination`, `InResponseTo` on Response **and** `SubjectConfirmationData`, `Recipient`, `NotBefore`/`NotOnOrAfter` with `MaxClockSkew`, `IssueInstant` ≤ 90 s old, `AudienceRestriction`, Status; decrypts `EncryptedAssertion` (AES-CBC, RSA-OAEP) with the SP key; `AllowIDPInitiated` flag (we leave it false) | `RetrieveAssertionInfo`: signatures, `Destination`, audience, time window with a pluggable clock, status, decryption. **No `InResponseTo` check anywhere** (`grep InResponseTo` in `validate.go`, `decode_response.go`, `retrieve_assertion.go` is empty); recipient check is absent too. We would own both | Everything is ours |
| IdP metadata parsing, SP metadata | `ParseMetadata`, `ServiceProvider.Metadata()`; AuthnRequest via Redirect or POST binding; also ships `samlidp` (a small IdP) and `testsaml` helpers, useful for the fake IdP | `types.EntityDescriptor` for unmarshalling, `Metadata()`; AuthnRequest Redirect binding | Ours |
| Caveats | Release cadence is slow (last tag 19 months ago at the time of writing); pins `goxmldsig v1.4.0`, which is **below** the GO-2026-4753 fix, so Harbormaster must require `goxmldsig v1.6.1` directly (MVS then selects 1.6.1; the scratch build confirmed the API is compatible). `MaxClockSkew` and `MaxIssueDelay` are package-level variables, so they are process-wide. Includes an IdP implementation and JWT-cookie middleware we do not use (not linked unless imported) | Most active; two 2026 advisories show it gets attention, and also that the SLO code path had a bypass. Missing `InResponseTo` and `Recipient` checks means the most important replay controls live in our code only | Highest effort, highest risk, no upside |

**Recommendation: `github.com/crewjam/saml` v0.5.1, core package only (not `samlsp`), with `github.com/russellhaering/goxmldsig` v1.6.1 required directly in `go.mod`.**

One-line rationale: it is the only option whose vetted code path already enforces every validation rule we need (`InResponseTo` on both response and subject confirmation, `Recipient`, `Destination`, audience, time window, single assertion, round-trip validation, decryption), so our own validator in `internal/saml` is a second, independent layer rather than the only one; gosaml2's release cadence is better but it leaves the replay-critical checks entirely to us. **Correction found in implementation (Task 7):** crewjam v0.5.1 does *not* reject multiple assertions (it returns the first that validates), accepts a missing `AudienceRestriction`, allows an omitted `Destination` on an unsigned Response, does not check NameID, AuthnStatement or confirmation method, and panics on a signed assertion without `Conditions`/`Subject`. Our layer enforces all of these, recovers library panics as `malformed`, and `TestCrewjamBaseline_AcceptsSecondUnsignedAssertion` pins the library behaviour so an upgrade that changes it is noticed.

Mitigations for the caveats, all in the plan: pin both modules; add a `make vulncheck` target running `govulncheck` (via `go run golang.org/x/vuln/cmd/govulncheck@latest`) and run it in the release target; keep every library call behind a small interface in `internal/saml` (`Verifier`) so a switch to gosaml2 touches one package; set `saml.MaxClockSkew` once at startup to the hard upper bound (300 s) and enforce the admin-configured skew in our validator.

What could **not** be verified: whether a vulnerability has been reported against crewjam/saml or gosaml2 outside the Go vulnerability database (GitHub advisories not yet mirrored, or vendor blogs); any behaviour of the four IdPs beyond their documentation (no IdP tenant was available); crewjam's release notes for v0.5.x beyond the tag dates. `govulncheck` was not installed in the session, so the "none open" statements rest on the database index, not on a scan of the call graph.

---

## Decisions

Each decision names the alternatives considered.

### D1. IdP-initiated sign-in: **not supported in v1**

The ACS rejects any response whose `InResponseTo` does not match a request row this Harbormaster issued (reason `unknown_request`). An unsolicited assertion cannot be bound to a browser or to a request, so it defeats the replay cache's time bound, the request binding cookie (D3) and login-CSRF protection in one go; crewjam's own comment on `AllowIDPInitiated` says as much. The common enterprise ask behind IdP-initiated SSO is "click the tile in Okta / My Apps / Google launcher". All four IdPs support that for SP-initiated apps: Okta "Login initiated by: Either Okta or App → App login URL"; Entra "Sign on URL" (the My Apps tile opens it); Google "Start URL"; Keycloak client "Home URL". Each is set to `{base_url}/api/auth/saml/login`. The README documents this per IdP. A future `allow_idp_initiated` switch is possible but is not designed here.

### D2. Single Logout: **out of scope; local logout only**

SP metadata advertises no `SingleLogoutService`. "Sign out" ends the Harbormaster session and shows the login page; the login page tells SSO users "You are signed out of Harbormaster. To sign out of your identity provider, sign out there." SLO reliability across IdPs is poor, it requires a signed `LogoutRequest`/`LogoutResponse` endpoint (where gosaml2's 2026 advisory lived), and the owner's threat (a shared workstation) is covered by the IdP's own session policy plus `SessionNotOnOrAfter` (D7).

### D3. Cross-task state: **Postgres rows plus a request-binding cookie**

Alternatives: (a) signed/encrypted cookie carrying request ID and return path, (b) Postgres rows, (c) both. The ACS is a cross-site top-level POST, so a `SameSite=Lax` cookie is **not** sent with it; a cookie would need `SameSite=None; Secure`, which plain-HTTP local runs cannot set. Postgres is where sessions already live and every task reaches it, so (b) is the source of truth for request state and the replay cache:

- `saml_requests(id text pk, return_to text, binding_hash text, test bool, created_at, expires_at, result jsonb)`: created by `GET /api/auth/saml/login`; consumed by the ACS with `DELETE … WHERE id = $1 AND expires_at > now() RETURNING …` so a request is accepted once, even if two tasks receive the same POST concurrently. TTL `HM_SAML_REQUEST_TTL` (default 10 m). `RelayState` carries the request ID only; the return path is never round-tripped through the IdP.
- `saml_assertion_ids(id text pk, expires_at timestamptz)`: insert of the accepted assertion's `ID` (and the response `ID`); a unique violation is a replay (reason `replay`). Rows expire at `NotOnOrAfter + skew` (capped at 24 h) and are removed by the hourly retention job together with expired `saml_requests`.

The cookie is defence in depth against **login CSRF** (an attacker completes a flow for their own IdP identity and makes the victim's browser POST the response): `hm_saml_req`, value = 32 random bytes (base64url), `HttpOnly`, `Path=/api/auth/saml/acs`, `Max-Age` = request TTL, `SameSite=None; Secure` when `HM_COOKIE_SECURE=true`, `SameSite=Lax` without `Secure` otherwise (the in-process fake IdP is same-site, so Lax is sent there). The row stores `sha256(cookie)`. The ACS requires the cookie to match the row (reason `binding_mismatch`); it never falls back. Because the cookie is first-party and read on a top-level navigation, third-party-cookie blocking does not affect it. A user with two sign-in tabs open gets two cookies with the same name; the browser sends the newest, so the older tab's response fails with `binding_mismatch` and the login page tells them to try again. Acceptable.

### D4. SP key pair and certificate: **generated on first save, stored in Postgres encrypted under a key derived from `HM_SESSION_KEY`**

Alternatives: (a) Terraform-managed Secrets Manager secret injected as env, (b) generated and stored encrypted in the database, (c) stored in plaintext in the database. (a) needs an IAM change and a TerraDuckTel run to create or rotate, contradicting owner decision 3, and puts a private key in Terraform state. (c) exposes the key to anyone with a database dump. (b): RSA-2048 key, self-signed X.509 certificate valid 10 years (`CN={host of base_url}`, SAN DNS), PEM encrypted with AES-256-GCM under `HKDF-SHA256(ikm = HM_SESSION_KEY, info = "harbormaster/saml-sp-key/v1")`, random 12-byte nonce per row. Stored in `saml_sp_keys(id, cert_pem, key_pem_enc, created_at, retired_at)`.

Consequences, all documented in the README: saving SSO configuration requires `HM_SESSION_KEY` to be set (the Terraform module already injects one; `make run` and the Playwright config gain a dev value), else the API answers 422 "Set HM_SESSION_KEY before configuring single sign-on."; rotating `HM_SESSION_KEY` makes the stored key undecryptable, the Settings page shows "The SP key cannot be decrypted (HM_SESSION_KEY changed). Generate a new certificate and update your identity provider." and SSO that needs the key (encrypted assertions) fails with reason `sp_key_unavailable` until an admin regenerates it. Rotation: "Generate new certificate" creates a new row, marks the previous one `retired_at = now()`; SP metadata lists the active certificate and, for 7 days, the retired one as a second `KeyDescriptor use="encryption"`, so assertions encrypted under either decrypt; retired rows older than 7 days are deleted by retention. Because AuthnRequests are unsigned (D5), rotation never breaks sign-in for IdPs that do not encrypt.

### D5. AuthnRequests are **unsigned**; assertions may be encrypted

Okta, Entra ID and Google do not verify AuthnRequest signatures by default; Keycloak's default for a new SAML client is "Client signature required: On" and the README tells Keycloak admins to turn it off (or to be the first to ask for signed requests). SP metadata sets `AuthnRequestsSigned="false"` and `WantAssertionsSigned="true"`. The AuthnRequest uses the HTTP-Redirect binding (deflate + base64 in the query string), the only binding all four IdPs accept for requests. Harbormaster never renders an auto-submitting form, so the CSP (`script-src 'self'`, `form-action 'self'`) stays unchanged; the fake IdP's auto-POST page loads its one-line script from its own path under `'self'`.

### D6. Identity model: **two independent credentials; identity keyed by NameID; never auto-link**

A user has up to two credentials: a **password** (`password_hash`, nullable) and an **SSO identity** (`idp_entity_id`, `external_id` = NameID value, `external_id_format`, nullable together). Every user has at least one (database constraint). `break_glass` means "may use the password while SSO is on" and therefore requires a password (constraint). There is no `auth_source` column; the UI derives "Password", "SSO linked", "Link pending" from the columns. Sessions still record how they were opened (`sessions.auth_source`).

Stable key: `(idp_entity_id, NameID value)`. Transient NameIDs are rejected (reason `transient_nameid`; Test sign-in surfaces it as "Your IdP issues a transient NameID; choose persistent, email or unspecified in the IdP app."). Email is **not** the key, because every IdP lets an admin or a self-service user change email, and some let two accounts share one over time.

Rules for an SSO sign-in (normal `login` purpose), in order:

1. `(entity_id, NameID)` **linked** to a user → sign that user in; refresh `email` and `display_name` from the attributes when present.
2. Else `(entity_id, NameID)` equals a user's **pending link** (D11, admin-set, not expired) → bind the identity to that user (set the three identity columns, clear the pending columns; the password, if any, stays), audit `sso_linked` with `method: "pending"`, sign in.
3. Else **JIT**: create a new user with no permissions, `is_admin = false`, `password_hash = NULL`, `must_change_password = false`. **It never attaches to an existing user** by username or email. The admin sees both users on the Users page and can link (D11) or delete the old one.
4. Username derivation for JIT: candidate = the configured username attribute if set and present, else the local part of the email attribute, else the NameID (local part if it contains `@`). Lowercase; every character outside `[a-z0-9._-]` becomes `-`; runs of `-` collapse; leading characters that are not a letter are dropped; empty → `sso-user`; truncate to 32. If taken (case-insensitive, the existing unique index), append `-2`, `-3`, … `-99`, trimming the base so the total stays ≤ 32. Usernames never change after creation or linking.
5. Password login: possible only for users with a password, and while SSO is on only for break-glass users (D8). A user without a password gets the generic error (dummy verify). "Change password" needs a current password, so it is unavailable (422 "This account has no password.") for password-less users; the admin's "Reset password" works for every user and reads "Set password" for users who have none, because it is how an admin gives a password-less user a password (for break-glass, or after turning SSO off).
6. A user who no longer exists at the IdP keeps their Harbormaster sessions until the shorter of idle (12 h), absolute (7 d) or `SessionNotOnOrAfter` (D7). Deleting the Harbormaster user ends sessions immediately, as today. A per-user "disabled" flag is out of scope.
7. Changing the IdP entity ID in the configuration orphans every linked identity and every pending link (both are scoped by entity ID). The Settings page warns: "Changing the identity provider signs out every SSO user and detaches every linked and pending SSO identity. Users keep their permissions and must be linked again, or will be created again at their next sign-in." Rare and explicit. Users with a password are detached (identity columns cleared) and pending links are cleared; password-less SSO users keep their old identity row, because clearing it would violate `users_has_credential`. That identity is scoped to the old entity ID, so it can no longer match any sign-in; an admin recovers such a user with unlink (temporary password) and then a new link.

### D11. Linking existing users (owner review: in scope now)

Two proven paths; no third.

**Self-link (primary; how the current admin links before enabling SSO).** A signed-in user with a password opens "Link SSO account" (sidebar footer), re-enters their **current password** in the dialog, and is sent through the normal SP-initiated flow with a request row of `purpose = 'link'` bound to their `user_id`. On a valid assertion the ACS binds `(entity_id, NameID)` to that user. It works while SSO is configured but **not yet enabled** (like Test sign-in), so the admin can link first and enable second.

- Start: `POST /api/auth/saml/link {"password": "…"}` with session and CSRF (a POST so a cross-site GET cannot start a link flow in the victim's browser); 401 `unauthenticated` "Current password is wrong." on a wrong password (existing `ErrWrongPassword`); 422 "This account has no password." when the user has none; 409 `already_linked` "This account is already linked to an SSO identity."; 404 when SSO is unconfigured. Answers `{"redirect_url": "https://idp/…"}`; the SPA navigates to it. The binding cookie is set as for any flow.
- Fresh re-auth: **the password in the request body is the re-authentication**; no time-window bookkeeping, and it also proves the second credential exactly when it is needed. Users with `must_change_password` are blocked by the existing gate until they change it.
- ACS (`purpose = 'link'`): after the full validation table, the identity must not be linked to any user (`sso_identity_taken`, also when it is another user's pending link); the row's user must still exist and still have no identity (`link_user_gone`, `link_user_has_identity`). Bind in one transaction; the user's current session continues (it is the one that just proved both credentials); other password sessions of that user are untouched; redirect to `/?sso_link=ok` (or the row's `return_to`), failures to `/?sso_link={reason}`. Audit `sso_linked` `{method: "self"}` with the user as actor; failures `sso_link_failed` with the reason.

**Admin pre-link (for everyone else).** On the Users page a global admin sets a **pending link** on a local user: the exact NameID the IdP will issue (shown by Test sign-in or the IdP console), under the configured IdP entity ID. The user's first SSO sign-in with that NameID binds to that account instead of creating a new one (D6 rule 2).

- `PUT /api/users/{id}/sso-link {"name_id": "…"}` (admin): user must have no identity (409 `already_linked`); the NameID must not be linked or pending on another user (409 `sso_identity_taken`); exact, case-sensitive match (SAML NameIDs are opaque strings; the UI says "Copy it exactly as the identity provider issues it. Test sign-in shows it."). `DELETE /api/users/{id}/sso-link` clears it. Audited `sso_link_pending_set` / `sso_link_pending_cleared` with the NameID in details (it is an identifier, not a secret).
- **Expiry: 14 days** (`pending_link_expires_at`). Email-format NameIDs get recycled when people leave and join; an unconsumed pending link is a latent takeover of the old user's permissions by the next holder of that NameID. After expiry the pending link is ignored at sign-in (JIT applies) and shown as "Link expired"; the admin sets it again. Not audited on expiry (nothing happened); audited when re-set.

**What linking does to the password: nothing (owner decision 7).** Linking, pending-link consumption and enabling SSO never touch `password_hash`. The gate is at sign-in time only: while SSO is on, `Login` refuses non-break-glass users with `sso_required` (D8); when SSO is off, everyone with a password can use it again immediately. Consequences:

- A linked user with a password holds two credentials; the password is dormant while SSO is on and is the fallback when SSO is turned off. The admin who links first and enables second keeps their password throughout.
- JIT users have no password unless an admin uses "Set password"; unlinking a password-less user sets a temporary password so nobody is left without a credential (below).
- Turning SSO **off** restores password sign-in for everyone who has one; only JIT users (and anyone an admin never gave a password) need "Set password". The disable confirm dialog says so.
- The enable confirm dialog tells the admin how many users with a password are not break-glass and will therefore be blocked from password sign-in (`SSOConfigDTO.password_users_blocked_on_enable`).
- Trade-off accepted by the owner: argon2id hashes of unused passwords stay in the database and keep being valid for the day SSO is turned off; they are not usable while SSO is on. The threat model records it.

**Break-glass and linking (reconciliation).** The earlier rule "SSO users cannot be break-glass" is **relaxed** to "break-glass requires a password" (constraint). Reason: the realistic break-glass holder is the operations lead who signs in with SSO every day and needs the password only in an outage; forcing them to keep a separate never-used local account makes that password rot. Consequences: "Make break-glass" on a password-less user is refused (422 "Set a password for {username} first.") and the button is disabled with that title; the enable guard (≥ 1 admin with `break_glass`, which implies a password) and the last-break-glass guards are unchanged; linking never removes break-glass or the password, so linking the last break-glass admin is allowed. The Settings page's enable guard message gains the hint "Create one with `harbormaster create-admin` or flag an existing admin who has a password."

**Consistency check after owner decision 7.** Lockout guard: unchanged (break-glass ⇒ password ⇒ usable while SSO is on). Unlink: still sets a temporary password only when the user has none (JIT users), because `users_has_credential` must hold. "Set password" / "Change password": unchanged (`has_password` drives both). DB constraints: unchanged; `users_has_credential` is now violated only by unlink-without-password, which the transaction prevents. Entity-ID change (D6 rule 7) detaches identities but never touches passwords, so previously linked users with a password can still sign in once SSO is off or if they are break-glass.

**Unlink (included, admin-only).** `POST /api/users/{id}/sso-unlink` removes the identity, ends that user's SAML sessions, and, when the user would be left without any credential, sets a temporary password in the same transaction and returns it once (`{"temporary_password": "…"}`, `must_change_password = true`), so the database constraint and the anti-goal both hold. Confirm copy differs for the two cases. Justification: wrong-identity links, IdP migrations and offboarding mistakes need a reversal that does not involve deleting the user and losing their permissions and history. Self-unlink is not offered: a compromised SSO session must not be able to detach the identity the admin set up.

### D7. Sessions

SSO sign-in creates a row in `sessions` through the same `createSession`; new columns `auth_source text not null default 'password'` and `not_after timestamptz null`. `not_after` = `SessionNotOnOrAfter` from the assertion's `AuthnStatement` when present (ignored when it is beyond the absolute limit, which `minTime` already handles). `Authenticate` treats `now > not_after` like idle expiry. Disabling SSO, changing the IdP entity ID, or replacing the IdP certificate set deletes every session with `auth_source = 'saml'` in the same transaction as the configuration write; saving other fields (label, attribute names, skew, metadata URL refresh with the same entity ID and a superset of certificates) does not. CSRF, idle touch, `/api/me` and sign-out are unchanged.

### D8. Lockout safety

- **Enable guard:** `enabled` can only become true when at least one user has `is_admin AND break_glass` (which implies a password). Otherwise 422 `invalid` "Flag at least one global admin as break-glass before turning on single sign-on." Enabling changes nothing else about users (D11: passwords are never cleared).
- **Last break-glass admin guard:** while SSO is enabled, removing break-glass from, demoting, or deleting a user would fail with the same 422 if it leaves zero break-glass admins. Implemented in `store` as `SetBreakGlassChecked`, extended `SetAdminChecked` and `DeleteUserChecked`, under the existing `lockAdminsTx` row lock (`select … for update`) so concurrent writes serialise.
- **Password sign-in while SSO is on:** `Login` verifies the password first, then, if the user is not break-glass, answers 403 `sso_required` "Use single sign-on to sign in to this account." (audited as `sign_in_failed`, reason `sso_required`). Revealing "this account exists and is not break-glass" to someone who already knows its password is acceptable; a wrong password still gets the generic message. Users without a password always get the generic message.
- **CLI recovery:** `harbormaster create-admin` creates break-glass admins (`break_glass = true`) so it works as the last resort when the only break-glass admin's password is lost. A new `harbormaster sso --disable` is **not** added: `create-admin` plus the UI covers recovery, and one fewer privileged subcommand is better.
- **Test before enable:** configuration is saved with `enabled = false`; "Test sign-in" starts a flow with `test = true`. The ACS validates everything exactly as a real sign-in would, but creates no user and no session; it writes the outcome (`ok`, NameID and format, attribute names and values, derived username, "would create user X" or "would sign in user Y", or the failure reason) into the request row's `result` and redirects to `/settings/sso?test={id}`. The SPA, in the admin's session, calls `GET /api/sso/tests/{id}` (admin only, one-time read, deletes the row) and shows the result. Because the admin's own browser does the round trip, a configuration mistake is visible before anyone is affected.

### D9. Metadata import safety

`POST /api/sso/metadata` accepts either `{"url": "https://…"}` or `{"xml": "…"}` (≤ 1 MiB). URL fetch: `https` required unless `HM_COOKIE_SECURE=false` (local dev, where the fake IdP is `http://127.0.0.1`); 10 s timeout; 1 MiB body cap; redirects followed at most 3 times and only to `https`; the resolved address must not be loopback, link-local (`169.254.0.0/16`, which includes the ECS task metadata endpoint and IMDS), or an RFC 1918 range unless the same dev flag is on. The parsed `IDPSSODescriptor` must contain exactly one entity ID, at least one signing certificate (certificates with `use="encryption"` only are ignored), and an `SingleSignOnService` with the HTTP-Redirect binding (the POST binding location is stored too but unused). Up to 5 signing certificates are kept (IdP rollover). Expired certificates are rejected on import; a certificate expiring within 30 days shows a warning on the Settings page.

### D10. Where the trust decision lives

`internal/saml.Verifier.Verify(ctx, Input{ResponseXML, RequestID, Now}) (Identity, error)` wraps crewjam's `ParseXMLResponse` and then re-checks, independently of the library: Issuer equals the configured entity ID; `Destination` equals the ACS URL derived from `HM_BASE_URL` (never from the `Host` header); `Recipient` equals the ACS URL; `InResponseTo` on the Response and on every `SubjectConfirmationData` equals `RequestID`; `NotBefore - skew ≤ now < NotOnOrAfter + skew` with the configured skew (default 120 s, bounded 0–300 s); audience contains the SP entity ID; exactly one assertion; NameID present and not transient; `AuthnStatement` present. Any discrepancy between the two layers is a hard failure with its own reason code (`validator_disagreement`), logged at error level, because it means a library behaviour changed.

---

## Flows

### SP-initiated sign-in (the only sign-in flow)

```
Browser                       Harbormaster task A            IdP                 Harbormaster task B
  |  GET /login                     |                          |                        |
  |  GET /api/auth/options -------->|  {sso:{enabled,label}}   |                        |
  |  click "Sign in with SSO"       |                          |                        |
  |  GET /api/auth/saml/login?return_to=/services/x           |                        |
  |-------------------------------->| insert saml_requests(id, return_to, sha256(c))    |
  |<-- 302 Location: IdP SSO URL?SAMLRequest=…&RelayState=id ; Set-Cookie hm_saml_req=c |
  |  GET IdP SSO URL ---------------------------------------->|                        |
  |  (IdP authenticates, MFA, …)                              |                        |
  |<-- 200 auto-POST form (SAMLResponse, RelayState=id) ------|                        |
  |  POST /api/auth/saml/acs  (cookie hm_saml_req=c; hm_session not sent: SameSite=Lax) |
  |-------------------------------------------------------------------------------------->|
  |                                 |                  DELETE saml_requests WHERE id RETURNING … (consumed once)
  |                                 |                  sha256(c) == binding_hash; Verify(response, id)
  |                                 |                  INSERT saml_assertion_ids (replay check)
  |                                 |                  find/create user; createSession(auth_source=saml, not_after)
  |<-- 303 Location: /services/x ; Set-Cookie hm_session=…; Set-Cookie hm_saml_req=; Max-Age=0 ----------|
  |  GET /api/me  → SPA renders                                                           |
```

Any failure after the request row is consumed redirects with `303 /login?error={sso_failed|sso_declined|sso_off}` and writes an audit entry with the precise reason. The user never sees XML or library errors.

### Test sign-in (admin, before enabling)

Same as above with `test = true` on the row; the ACS stops after `Verify`, computes the would-be outcome and writes it to `saml_requests.result`, then redirects to `/settings/sso?test={id}`. No user, no session, no `saml_assertion_ids` insert (a real sign-in with the same assertion is impossible anyway because the request row is gone).

### Self-link (signed-in user, SSO configured, enabled or not)

```
SPA (signed in as pavel)         Harbormaster                         IdP
  | "Link SSO account" dialog: password                                 |
  | POST /api/auth/saml/link {password}  (hm_session + X-CSRF-Token) -->|
  |            verify password; insert saml_requests(purpose=link, user_id=pavel, binding)
  |<-- 200 {"redirect_url": IdP?SAMLRequest…&RelayState=id}; Set-Cookie hm_saml_req
  | window.location.assign(redirect_url) ---------------------------------------------->|
  |<-- auto-POST SAMLResponse ----------------------------------------------------------|
  | POST /api/auth/saml/acs (hm_saml_req) -->  consume row (purpose=link) ; Verify
  |            identity unlinked? user still has no identity? → bind (password untouched)
  |<-- 303 /?sso_link=ok   (existing hm_session keeps working)
```

Pending-link consumption has no flow of its own: it is the normal sign-in flow with D6 rule 2 applied at the ACS.

### Password sign-in while SSO is on

`POST /api/auth/login` unchanged for break-glass users; audited as `sign_in` with `details.break_glass = true` and summary "Signed in with password (break-glass)." For other local users: 403 `sso_required` after a correct password; generic 401 after a wrong one. For SSO users: generic 401 (NULL hash → dummy verify).

### Sign-out

`POST /api/auth/logout` unchanged. The login page, when it was reached by signing out (`?signed_out=1`) and SSO is enabled, shows the D2 sentence.

---

## Validation rules (every ACS POST)

| # | Check | Failure reason (audit `details.reason`) | User sees |
|---|---|---|---|
| 1 | `Content-Type: application/x-www-form-urlencoded`, body ≤ 256 KiB, `SAMLResponse` present and base64 | `malformed` | `sso_failed` |
| 2 | SSO enabled, or the request row's purpose is `test` or `link` | `sso_off` | `sso_off` |
| 3 | Request row found by `InResponseTo` and not expired, consumed atomically | `unknown_request` (also covers IdP-initiated) | `sso_failed` |
| 4 | `hm_saml_req` cookie present and `sha256(cookie) == binding_hash` | `binding_mismatch` | `sso_failed` |
| 5 | `RelayState` equals the request ID; if not, logged at warn and ignored (some IdPs rewrite it); never used as a URL | — | — |
| 6 | XML round-trip validation, exactly one `Response`, at most one `Assertion` or `EncryptedAssertion` | `malformed`, `multiple_assertions` | `sso_failed` |
| 7 | `Status/StatusCode` is `urn:oasis:names:tc:SAML:2.0:status:Success` | `idp_status` (with the IdP's code in details) | `sso_declined` |
| 8 | Signature valid under one of the configured IdP signing certificates on the `Response` or the `Assertion` (the signed element must enclose the assertion that is used). Unsigned both → reject | `unsigned`, `invalid_signature` | `sso_failed` |
| 9 | `EncryptedAssertion` decrypts with the active or retired SP key; unsupported algorithm → reject | `decrypt_failed`, `sp_key_unavailable` | `sso_failed` |
| 10 | `Issuer` (Response and Assertion) equals the configured IdP entity ID | `issuer` | `sso_failed` |
| 11 | `Destination` equals `{base_url}/api/auth/saml/acs` | `destination` | `sso_failed` |
| 12 | Every `SubjectConfirmation` is `bearer` with `Recipient` = ACS URL, `InResponseTo` = request ID, `NotOnOrAfter + skew > now` | `recipient`, `in_response_to`, `expired` | `sso_failed` |
| 13 | `Conditions.NotBefore - skew ≤ now < NotOnOrAfter + skew`, skew = configured (default 120 s, max 300 s) | `not_yet_valid`, `expired` | `sso_failed` |
| 14 | `AudienceRestriction` contains the SP entity ID (when present it is required to match; crewjam treats "no restriction" as valid, our layer **requires** at least one restriction) | `audience` | `sso_failed` |
| 15 | `IssueInstant` within 90 s (crewjam `MaxIssueDelay`) | `expired` | `sso_failed` |
| 16 | Assertion `ID` and Response `ID` not in `saml_assertion_ids` (insert; unique violation = replay) | `replay` | `sso_failed` |
| 17 | `Subject/NameID` present, format not `…:nameid-format:transient` | `no_nameid`, `transient_nameid` | `sso_failed` |
| 18 | `AuthnStatement` present; `SessionNotOnOrAfter` parsed if present | `no_authn_statement` | `sso_failed` |
| 19 | Purpose `login`: linked user → sign in; pending link match → bind (D6 rule 2); else JIT create. Purpose `link`: identity not linked or pending elsewhere, row's user exists and has no identity → bind (D11) | `user_create_failed`, `sso_identity_taken`, `link_user_gone`, `link_user_has_identity` | `sso_failed`; link failures go to `/?sso_link={reason}` |
| 20 | Return path from the row is a relative path: starts with `/`, not `//`, no `\`, no scheme; otherwise `/` | — | — |

Reason codes are the closed set above plus `validator_disagreement` (D10) and `internal`. The three `link_*`/`sso_identity_taken` codes are also the `?sso_link=` values the SPA turns into toasts. The audit summary is "SSO sign-in failed ({reason})." with the NameID when one could be read.

---

## Threat model summary

| Threat | Mitigation | Where |
|---|---|---|
| Forged or tampered assertion | XML-DSig over Response or Assertion with the configured IdP certificates only; round-trip validation; single assertion; our second validator (D10) | `internal/saml` Verifier, rules 6, 8, 14 |
| Signature wrapping (valid signature on one element, attacker content in another) | goxmldsig ≥ 1.6.1 "use only the validated element" model; crewjam rejects multiple assertions; our layer re-reads NameID and conditions from the element crewjam returned, never from the raw document | rules 6, 8 |
| Replay of a captured ACS POST | One-time request row (`DELETE … RETURNING`), assertion and response ID cache, `NotOnOrAfter` window | rules 3, 13, 16 |
| IdP-initiated or cross-SP assertion ("an assertion for another SP at the same IdP") | `InResponseTo` must match our request; audience must contain our entity ID; `Destination`/`Recipient` must equal our ACS | rules 3, 11, 12, 14 |
| Login CSRF (attacker signs the victim's browser into the attacker's account) | `hm_saml_req` binding cookie must match the request row | rule 4, D3 |
| Open redirect via `RelayState` | `RelayState` carries only the request ID; return path stored server-side and validated as a relative path | rules 5, 20 |
| Account takeover through username or email collision | Never auto-link; link only by `(entity_id, NameID)`; transient NameIDs rejected | D6 |
| Attacker links their IdP identity to a victim's account | Self-link starts only with a CSRF-protected POST carrying the victim's current password; the request row is bound to the user ID and to the binding cookie; the ACS re-checks the identity is unlinked at bind time (unique index) | D11, rule 4 |
| Admin typo or recycled NameID in a pending link binds the wrong person | Exact match only; admin-only; NameID shown by Test sign-in; 14-day expiry; unique per NameID; audited on set, clear and consumption | D11 |
| Linking or unlinking strands a user without any credential | DB constraint "password or identity"; linking never removes a password; unlink sets a temporary password when the user has none | D11 |
| Dormant passwords of linked users remain valid hashes while SSO is on | **Accepted by the owner (decision 7).** They are unusable while SSO is on (`sso_required` after a correct password); existing per-user lock and per-IP limits still apply to guesses; the Users page shows who holds a password so admins can prune; turning SSO off is the only path that makes them usable again | D8, D11 |
| Compromised SSO session detaches the admin-set identity | No self-unlink; unlink is admin-only and audited | D11 |
| Privilege via JIT | New users have no permissions and are not admin | D6, owner decision 2 |
| Lockout | Enable guard, last break-glass admin guard, `create-admin` creates break-glass admins, test sign-in | D8 |
| Stale IdP sessions after offboarding | `SessionNotOnOrAfter` honoured; idle 12 h / absolute 7 d; admin can delete the user | D7 |
| Metadata URL used for SSRF against the task metadata endpoint or VPC | https-only, no loopback/link-local/RFC 1918 in production, size and time caps, redirect limits | D9 |
| Stolen database dump reveals the SP private key | AES-256-GCM under HKDF(`HM_SESSION_KEY`); the key itself is in Secrets Manager via Terraform | D4 |
| Password brute force on break-glass accounts | Existing per-user lock (5/15 min) and per-IP limit; break-glass accounts are few and are visible on the Users page | existing `Login` |
| ACS as a DoS vector (expensive XML/RSA per POST, unauthenticated) | Body cap, per-IP limit (30 ACS failures / 15 min through `login_attempts` with username `sso:<ip>`), per-IP token bucket on `GET /api/auth/saml/login` (60/min per task), request row TTL | API |
| CSRF on the ACS itself | The ACS reads no session cookie and performs no action in the context of an existing session; it only creates a session for the identity the IdP asserted, bound to a request this browser started. That is why it is the only non-GET route exempt from `RequireCSRF` besides login and setup | API |
| Clickjacking of the login page or ACS in an iframe | Existing `frame-ancestors 'none'` / `X-Frame-Options: DENY` | existing headers |
| Error pages leak XML or library internals | Fixed set of three user-facing messages; details only in the audit log and server log | API |

---

## Data model (migration `0070_saml.up.sql`)

Plan 07 follows the existing step of ten (0060 → 0070). No down migrations.

```sql
-- users: two independent credentials (D6, D11)
alter table users alter column password_hash drop not null;              -- NULL = no password
alter table users add column break_glass bool not null default false;
alter table users add column idp_entity_id text;                          -- the three identity columns are NULL or set together
alter table users add column external_id text;                            -- NameID value
alter table users add column external_id_format text;
alter table users add column email text;
alter table users add column display_name text;
alter table users add column pending_link_name_id text;                   -- admin pre-link (D11)
alter table users add column pending_link_set_at timestamptz;
alter table users add column pending_link_expires_at timestamptz;
alter table users add column pending_link_set_by_user_id bigint;
create unique index users_external_id_idx on users (idp_entity_id, external_id) where external_id is not null;
create unique index users_pending_link_idx on users (pending_link_name_id) where pending_link_name_id is not null;
alter table users add constraint users_identity_shape check (
  (idp_entity_id is null and external_id is null and external_id_format is null) or
  (idp_entity_id is not null and external_id is not null and external_id_format is not null));
alter table users add constraint users_has_credential check (password_hash is not null or external_id is not null);
alter table users add constraint users_break_glass_needs_password check (not break_glass or password_hash is not null);
alter table users add constraint users_pending_needs_unlinked check (pending_link_name_id is null or external_id is null);
alter table users add constraint users_pending_shape check (
  (pending_link_name_id is null) = (pending_link_expires_at is null));

-- sessions
alter table sessions add column auth_source text not null default 'password'
  check (auth_source in ('password','saml'));
alter table sessions add column not_after timestamptz;

-- single-row configuration
create table saml_config (
  id smallint primary key check (id = 1),
  enabled bool not null default false,
  idp_entity_id text not null,
  idp_sso_redirect_url text not null,
  idp_sso_post_url text,
  idp_certs text[] not null,                     -- PEM, 1..5 signing certificates
  metadata_url text,
  metadata_xml text,
  metadata_fetched_at timestamptz,
  sp_entity_id text not null,                    -- default {base_url}/api/auth/saml/metadata
  name_id_format text not null default 'unspecified'
    check (name_id_format in ('unspecified','email','persistent')),
  attr_email text,                               -- NULL = try the well-known names
  attr_display_name text,
  attr_username text,                            -- NULL = derive (D6 rule 3)
  clock_skew_seconds int not null default 120 check (clock_skew_seconds between 0 and 300),
  button_label text not null default 'Sign in with SSO',
  updated_at timestamptz not null,
  updated_by_user_id bigint
);

create table saml_sp_keys (
  id bigserial primary key,
  cert_pem text not null,
  key_pem_enc bytea not null,                    -- AES-256-GCM(nonce || ciphertext)
  created_at timestamptz not null default now(),
  retired_at timestamptz
);

create table saml_requests (
  id text primary key,                           -- AuthnRequest ID ("_" + 32 random bytes hex)
  purpose text not null default 'login' check (purpose in ('login','test','link')),
  user_id bigint references users on delete cascade,   -- set for purpose = 'link' only
  return_to text not null,
  binding_hash text not null,                    -- sha256 of the hm_saml_req cookie value
  created_at timestamptz not null default now(),
  expires_at timestamptz not null,
  result jsonb,                                  -- test outcome, read once
  constraint saml_requests_link_user check ((purpose = 'link') = (user_id is not null))
);
create index saml_requests_expires_idx on saml_requests (expires_at);

create table saml_assertion_ids (
  id text primary key,
  expires_at timestamptz not null
);
create index saml_assertion_ids_expires_idx on saml_assertion_ids (expires_at);
```

The `settings` key/value table is not used for SAML: the configuration has a shape, a check constraint set and a transaction with session deletion, which a typed table expresses and a text value does not.

Retention (hourly, existing job): `saml_requests` where `expires_at < now()`; `saml_assertion_ids` where `expires_at < now()`; `saml_sp_keys` where `retired_at < now() - 7 days`.

---

## API

Public routes are `routeOptions{Public: true}` (no session, no CSRF, no password-change gate). The ACS additionally reads form bodies, not JSON, so it does not go through `decodeJSON`.

| Method and path | Auth | Behaviour |
|---|---|---|
| `GET /api/auth/options` | public | `{"sso": {"enabled": bool, "label": string} \| null, "password_login": "all" \| "break_glass"}`. `sso` is null when no configuration row exists. `password_login` is `break_glass` iff SSO is enabled |
| `GET /api/auth/saml/login?return_to=/path&test=0` | public; `NoETag` | Creates the request row, sets `hm_saml_req`, answers `302` to the IdP redirect URL with `SAMLRequest` and `RelayState`. `404 not_found` when SSO is not enabled, except `test=1` which requires an admin session and an existing configuration (test works while disabled). Per-IP token bucket 60/min, `429` `rate_limited` beyond it |
| `POST /api/auth/saml/acs` | public; **CSRF-exempt**; `NoETag` | Form body. Runs the validation table; on success `303` to the return path with the session cookie; on failure `303 /login?error=…`. For test rows, `303 /settings/sso?test={id}`. For link rows, `303 /?sso_link=ok` (or the row's return path) and on failure `303 /?sso_link={reason}`; no session cookie is set or read. Never answers JSON to a browser except `405` for non-POST |
| `POST /api/auth/saml/link` | user (session + CSRF) | `{"password": "…"}` (strict JSON). Verifies the current password (401 "Current password is wrong."), requires a password (422 "This account has no password."), no identity yet (409 `already_linked`), SSO configured (404; enabled not required). Creates a `purpose = 'link'` row bound to the user, sets `hm_saml_req`, answers `{"redirect_url": …}`. Same per-IP bucket as the login start |
| `GET /api/auth/saml/metadata` | public | SP `EntityDescriptor` XML, `Content-Type: application/samlmetadata+xml`, `Content-Disposition: attachment; filename="harbormaster-sp-metadata.xml"`. `404` when no configuration row exists. Includes `AssertionConsumerService` (POST binding, index 0, default), `NameIDFormat` per config, `KeyDescriptor use="encryption"` for the active (and ≤ 7-day retired) certificate, `AuthnRequestsSigned="false"`, `WantAssertionsSigned="true"` |
| `GET /api/sso` | admin | `SSOConfigDTO` (below) |
| `PUT /api/sso` | admin | Saves the manual fields: `idp_entity_id`, `idp_sso_redirect_url`, `idp_certs` (PEM array), `name_id_format`, `attr_email`, `attr_display_name`, `attr_username`, `clock_skew_seconds`, `button_label`. Strict JSON. Creates the SP key pair on the first save (422 when `HM_SESSION_KEY` is empty). Changing `idp_entity_id` or dropping a certificate that was in the set deletes SAML sessions (D7). Audited `sso_config_update` with the changed field names (never certificate or key material) |
| `POST /api/sso/metadata` | admin | `{"url": …}` or `{"xml": …}` per D9. Parses, validates, stores `metadata_url`/`metadata_xml`/`metadata_fetched_at` and overwrites `idp_entity_id`, `idp_sso_redirect_url`, `idp_sso_post_url`, `idp_certs`. Returns `SSOConfigDTO`. 422 with a precise message on parse errors ("Metadata has no IDPSSODescriptor.", "Metadata has no HTTP-Redirect SingleSignOnService.", "Certificate 1 expired on …", "Fetching metadata needs an https URL.") |
| `PUT /api/sso/enabled` | admin | `{"enabled": bool}`. Enable guard (D8); answers the updated `SSOConfigDTO`. Disabling deletes SAML sessions. Audited `sso_enabled` / `sso_disabled` |
| `POST /api/sso/key` | admin | Generates a new SP key pair, retires the current one. Audited `sso_key_rotated` with the new fingerprint |
| `GET /api/sso/tests/{id}` | admin | Returns and deletes the test result; `404` if absent, consumed or not a test row |
| `POST /api/users/{id}/break-glass` | admin | `{"break_glass": bool}`. 422 "Set a password for {username} first." for password-less users; last-break-glass-admin guard while SSO is on. Audited `break_glass_grant` / `break_glass_revoke` |
| `PUT /api/users/{id}/sso-link` | admin | `{"name_id": "…"}` (1–256 chars, trimmed, exact). 409 `already_linked` when the user has an identity; 409 `sso_identity_taken` when the NameID is linked or pending on another user. Sets `pending_link_*` with a 14-day expiry; re-setting replaces. Audited `sso_link_pending_set` |
| `DELETE /api/users/{id}/sso-link` | admin | Clears the pending link (204; 404 when none). Audited `sso_link_pending_cleared` |
| `POST /api/users/{id}/sso-unlink` | admin | Removes the identity and ends the user's SAML sessions; when the user has no password, sets a temporary one (`must_change_password = true`) in the same transaction and returns `{"temporary_password": "…"}`, else `{}`. 404 when not linked. Audited `sso_unlinked` `{password_set: bool}` |
| `POST /api/users/{id}/reset-password` | admin | **Unchanged behaviour**, now also valid for password-less users (it is how they get one); the UI labels it "Set password" for them |
| `POST /api/auth/password` | user | **Changed:** 422 "This account has no password." for password-less users |
| `GET /api/users`, `GET /api/me` | | **Changed DTOs** below |

DTO changes (snake_case, contract golden regenerated):

```go
// UserDTO gains
HasPassword          bool       `json:"has_password"`
BreakGlass           bool       `json:"break_glass"`
SSO                  string     `json:"sso"`                    // "none" | "linked" | "pending" | "pending_expired"
SSONameID            *string    `json:"sso_name_id"`            // linked or pending NameID (admin-visible identifier)
PendingLinkExpiresAt *time.Time `json:"pending_link_expires_at"`
Email                *string    `json:"email"`
DisplayName          *string    `json:"display_name"`

// MeDTO gains
HasPassword bool    `json:"has_password"`
SSOLinked   bool    `json:"sso_linked"`
BreakGlass  bool    `json:"break_glass"`
Email       *string `json:"email"`
DisplayName *string `json:"display_name"`

type SSOConfigDTO struct {
    Configured         bool        `json:"configured"`
    Enabled            bool        `json:"enabled"`
    IdPEntityID        string      `json:"idp_entity_id"`
    IdPSSORedirectURL  string      `json:"idp_sso_redirect_url"`
    IdPCerts           []CertDTO   `json:"idp_certs"`          // fingerprint, subject, not_after; never the PEM back out
    MetadataURL        *string     `json:"metadata_url"`
    MetadataFetchedAt  *time.Time  `json:"metadata_fetched_at"`
    SPEntityID         string      `json:"sp_entity_id"`
    ACSURL             string      `json:"acs_url"`
    MetadataPath       string      `json:"metadata_path"`      // "/api/auth/saml/metadata"
    SPCert             *CertDTO    `json:"sp_cert"`
    SPKeyState         string      `json:"sp_key_state"`       // "ok" | "missing" | "undecryptable"
    NameIDFormat       string      `json:"name_id_format"`
    AttrEmail          *string     `json:"attr_email"`
    AttrDisplayName    *string     `json:"attr_display_name"`
    AttrUsername       *string     `json:"attr_username"`
    ClockSkewSeconds   int         `json:"clock_skew_seconds"`
    ButtonLabel        string      `json:"button_label"`
    BreakGlassAdmins   int         `json:"break_glass_admins"` // drives the enable guard in the UI
    PasswordUsersBlockedOnEnable int `json:"password_users_blocked_on_enable"` // users with a password and not break-glass (shown in the enable confirm)
    Warnings           []string    `json:"warnings"`           // "Certificate … expires in 12 days.", key state messages
}
type CertDTO struct{ Fingerprint, Subject string; NotAfter time.Time } // sha256 fingerprint, colon-separated
type SSOTestResultDTO struct {
    OK           bool              `json:"ok"`
    Reason       string            `json:"reason"`         // "" when ok
    Message      string            `json:"message"`        // admin-facing explanation
    NameID       string            `json:"name_id"`
    NameIDFormat string            `json:"name_id_format"`
    Attributes   map[string][]string `json:"attributes"`
    Username     string            `json:"username"`       // derived (would_create) or existing
    Outcome      string            `json:"outcome"`        // "would_create" | "would_sign_in" | "would_link" (pending link matches)
    ExistingUser *string           `json:"existing_user"`
}
```

Error codes: existing `invalid` 422, `forbidden` 403, `not_found` 404, `conflict` 409 (kept for generic conflicts); new `rate_limited` 429 for the per-IP bucket on `GET /api/auth/saml/login` and `POST /api/auth/saml/link` (the existing `throttled` code means "AWS is throttling us" and is a 503, so it is not reused); new `sso_required` 403 on `POST /api/auth/login`; new `already_linked` 409 and `sso_identity_taken` 409 on the link endpoints.

Security headers: unchanged. `Cache-Control: no-store` already covers `/api/*`, so SP metadata and the options endpoint are not cached by proxies.

Rate limiting: `GET /api/auth/saml/login` per-IP token bucket (60/min, in-process, per task); ACS failures recorded in `login_attempts` as username `sso:<ip>` so the existing `LoginMaxPerIP` (30/15 min) applies to the ACS too; successes are not recorded there.

---

## Sessions, config and environment

- New env: `HM_SAML_REQUEST_TTL` (default `10m`, bounds 1–30 m). Everything else about SAML is in the database by owner decision 3.
- Existing env that now matters more: `HM_SESSION_KEY` (required to save SSO configuration), `HM_BASE_URL` (entity ID, ACS URL, `Destination` check; must be the URL the browser uses), `HM_COOKIE_SECURE` (controls the binding cookie's `SameSite`/`Secure` and the dev-only http metadata allowance).
- `cmd/harbormaster serve`: constructs `saml.Service` from the store and config; under `--fake-aws` also mounts the fake IdP at `/fake-idp/*` (outside `/api`, outside session auth, inside security headers). `create-admin` always creates break-glass admins (owner decision).
- Terraform module: no new resources. `HM_SESSION_KEY` is already injected; `base_url` is already required to be https. README additions: the IdP never talks to Harbormaster directly (POST binding goes through the browser), so an internal ALB and a tight `allowed_cidrs` work; the ACS and metadata URLs to paste into the IdP; the statement that no Terraform change is needed to enable SSO. `extra_env` validation gains nothing (`HM_SAML_REQUEST_TTL` is allowed).

---

## UI

Copy below is verbatim (convention: spec wording is binding).

### Login page (`/login`)

- Fetches `GET /api/auth/options` once. While loading, renders nothing but the card frame.
- `sso.enabled`: primary button with the configured label (default **"Sign in with SSO"**) that navigates to `/api/auth/saml/login?return_to={from}`. Below it a link **"Sign in with a password instead"** that reveals the existing form plus the note **"Only break-glass accounts can sign in with a password."** The form stays hidden by default; the e2e helper `login()` clicks the link when it is present.
- `sso` null or disabled: today's form, unchanged.
- `?error=sso_failed` → alert **"Single sign-on failed. Try again, or ask a global admin to check the audit log."**; `?error=sso_declined` → **"Your identity provider declined the sign-in."**; `?error=sso_off` → **"Single sign-on is turned off."**; `?signed_out=1` with SSO enabled → info line **"You are signed out of Harbormaster. To sign out of your identity provider, sign out there."**
- `sso_required` from the password form → the error's message as returned ("Use single sign-on to sign in to this account.").

### Settings → new tab **"Single sign-on"** (`/settings/sso`, admin only)

Cards, top to bottom:

1. **Status** — pill **"SSO on"** / **"SSO off"**, toggle button **"Turn on single sign-on"** / **"Turn off single sign-on"**. Turning on when `break_glass_admins == 0` is disabled with title **"Flag at least one global admin as break-glass before turning on single sign-on. Create one with harbormaster create-admin, or flag an existing admin who has a password."** Turning on opens a confirm: title **"Turn on single sign-on?"**, sub **"{n} users have a password but are not break-glass; they can only sign in with SSO while it is on. Passwords are kept and work again if you turn it off."** (with `n` = `password_users_blocked_on_enable`; when `n == 0`: **"Only break-glass accounts can sign in with a password while single sign-on is on."**), confirm **"Turn on"**. Turning off opens a confirm: title **"Turn off single sign-on?"**, sub **"Everyone signed in with SSO is signed out now. Users with a password can sign in with it again; users without one need an admin to set one."**, confirm **"Turn off"**. Warnings from the DTO render here as a `Banner`. When the signed-in admin is not linked yet, a hint line: **"Link your own account first so you can sign in with SSO: use Link SSO account in the sidebar."**
2. **Identity provider** — `Seg` with **"Metadata URL"** / **"Metadata file"** / **"Manual"**. URL: input + **"Fetch"**; file: `<input type="file" accept=".xml">` + **"Upload"**; manual: entity ID, SSO URL (HTTP-Redirect), certificate PEM textarea (one or more). Below, read-only summary: entity ID, SSO URL, certificates (subject, fingerprint, expiry), **"Last fetched {ago}"** and **"Refresh"** when a URL is stored. Changing the entity ID shows the D6 rule 6 warning before save.
3. **Service provider** — read-only entity ID, ACS URL, **"Download SP metadata"** (link to `/api/auth/saml/metadata`), SP certificate subject/fingerprint/expiry, **"Generate new certificate"** with confirm sub **"The current certificate keeps decrypting assertions for 7 days. Update your identity provider with the new SP metadata."** Key state messages from D4.
4. **Sign-in options** — button label, NameID format select (**"Let the IdP choose"**, **"Email address"**, **"Persistent"**), attribute names for email, display name, username (placeholders show the well-known defaults), clock skew (seconds, 0–300). **"Save"**.
5. **Test sign-in** — **"Test sign-in"** opens `/api/auth/saml/login?test=1` in a new tab. When the page loads with `?test={id}` it fetches the result and renders: success → **"Sign-in test passed."** with NameID, format, attributes table, **"Would create user {username}"**, **"Would sign in {username}"** or **"Would link {username} (pending link)"**; failure → **"Sign-in test failed: {message}"** with the reason code in monospace. A **"Dismiss"** button clears the query parameter. The NameID is shown in a copyable monospace field with the caption **"Use this value for pending links."**

`SETTINGS_TABS` gains `['sso', 'Single sign-on']` after `users`; `AdminOnly` gains area `sso` with copy **"Only global admins change single sign-on."**

### Users page

- New column **"Sign-in"**: pills derived from the DTO: **"Password"** (mute) when `has_password`; **"SSO linked"** (info) when `sso === 'linked'`; **"Link pending"** (warning, title **"Pending link to {name_id}, expires {date}"**) when `pending`; **"Link expired"** (mute) when `pending_expired`; **"Break-glass"** (warning) when `break_glass`. Email shown under the username in muted small text when present; display name as the title attribute of the username cell.
- The subtitle under the heading becomes: SSO off → **"Global admins can do everything. Every other user gets permissions per tag. Sign-in is username and password."** (unchanged); SSO on → **"Global admins can do everything. Every other user gets permissions per tag. Sign-in is single sign-on; break-glass accounts can also use a password."**
- Actions:
  - **"Reset password"**, labelled **"Set password"** when `!has_password`; confirm sub for that case **"They get a temporary password and must choose their own at next sign-in. While single sign-on is on, only break-glass accounts can use it."**
  - **"Make break-glass"** / **"Remove break-glass"**; disabled with title **"Set a password for {username} first."** when `!has_password`; confirm title **"Make {username} a break-glass account?"**, sub **"Break-glass accounts can sign in with a password even when single sign-on is on. Keep this to a few global admins."**, confirm **"Make break-glass"**; removal confirm **"Remove break-glass from {username}?"** / **"Remove break-glass"**. The guard's 422 message surfaces in the toast as today.
  - **"Link SSO…"** when `sso` is `none` or `pending_expired` (and SSO is configured): dialog title **"Link {username} to an SSO identity"**, field **"NameID"** with hint **"Copy it exactly as the identity provider issues it. Test sign-in shows it."**, body **"At their next SSO sign-in with this NameID, {username} keeps this account and its permissions instead of getting a new one. The link expires in 14 days."**, confirm **"Set pending link"**. For `pending` the action reads **"Change pending link…"** and a second action **"Clear pending link"** (no confirm; toast **"Pending link cleared."**).
  - **"Unlink SSO…"** when `sso === 'linked'`: confirm title **"Unlink {username} from SSO?"**; sub when `has_password` **"Their SSO sessions end now. They keep their password."**, when `!has_password` **"Their SSO sessions end now. They get a temporary password and must choose their own at next sign-in."**; confirm **"Unlink"**. A returned temporary password is shown in the same sticky toast style as Reset password.
- **Add user** unchanged (creates a local user). A hint under the dialog when SSO is on: **"New local users can only sign in with a password if you make them break-glass. SSO users are created automatically at their first sign-in, or set a pending link to attach one to an existing account."**

### Sidebar footer and the self-link dialog

- **"Change password"** is hidden when `!me.has_password`. `/password` for such a user redirects to `/`.
- **"Link SSO account"** appears when SSO is configured (`auth options` `sso !== null`), `me.has_password` and `!me.sso_linked`. It opens a `Dialog`: title **"Link your SSO account"**, body **"You will sign in at your identity provider; the account you sign in with there is attached to this Harbormaster user. Enter your password to continue."**, field **"Password"**, confirm **"Continue to identity provider"**. On success the SPA navigates to `redirect_url`. Errors show the API message inline.
- When the app loads with `?sso_link=ok` it shows the toast **"Your SSO account is linked."** and refreshes `/api/me`; `?sso_link=sso_identity_taken` → **"That SSO identity is already linked to another user."**; any other value → **"Linking failed. Ask a global admin to check the audit log."**; the parameter is removed from the URL.
- My access, audit log: unchanged except that new audit actions appear in the list.

---

## Audit log events

| Action | Actor | Summary | Details |
|---|---|---|---|
| `sso_sign_in` | the user | `Signed in with SSO.` | `name_id_format`, `jit` (bool) |
| `sso_sign_in_failed` | none (username `sso:<name_id or ->`) | `SSO sign-in failed ({reason}).` | `reason`, `idp_status` when relevant, `in_response_to` |
| `sso_user_created` | the new user | `Created user {username} from SSO.` | `email`, `name_id_format` |
| `sso_linked` | the user (self) or the user (pending, actor = the user signing in) | `Linked {username} to SSO identity {name_id}.` | `method: "self" \| "pending"`, `has_password` (bool), `pending_set_by_user_id` for pending |
| `sso_link_failed` | the row's user (self-link) | `SSO link failed ({reason}).` | `reason`, `name_id` when read |
| `sso_link_pending_set` / `sso_link_pending_cleared` | admin | `Set a pending SSO link for {u} to {name_id}.` / `Cleared the pending SSO link of {u}.` | `name_id`, `expires_at` |
| `sso_unlinked` | admin | `Unlinked {u} from SSO identity {name_id}.` | `name_id`, `password_set` (bool), `sessions_ended` |
| `sso_test` | admin | `Ran an SSO sign-in test ({ok\|reason}).` | `reason`, `would_create`/`would_sign_in` |
| `sso_config_update` | admin | `Changed single sign-on settings: {fields}.` | `fields` (names only), `idp_entity_id_changed`, `sessions_ended` |
| `sso_metadata_import` | admin | `Imported IdP metadata from {url\|file}.` | `idp_entity_id`, `cert_fingerprints` |
| `sso_enabled` / `sso_disabled` | admin | `Turned single sign-on on.` / `Turned single sign-on off.` | `sessions_ended` (on disable) |
| `sso_key_rotated` | admin | `Generated a new SP certificate.` | `fingerprint` |
| `break_glass_grant` / `break_glass_revoke` | admin | `Made {u} a break-glass account.` / `Removed break-glass from {u}.` | — |
| `sign_in` (existing) | the user | `Signed in with password (break-glass).` when SSO is on | `break_glass: true` |
| `sign_in_failed` (existing) | — | `Failed sign-in for {u}.` | `reason: "sso_required"` added for that case |

---

## Local development and tests (no real IdP)

- **`internal/saml/fakeidp`**: an in-process IdP built on crewjam's `saml.IdentityProvider` with a `SessionProvider` that reads the username from a form. Routes: `GET /fake-idp/metadata` (IdP metadata), `GET /fake-idp/sso` (shows a one-field form **"Sign in as"** pre-filled from `?user=`; on submit, or immediately when `?auto=1`, issues a signed Response for `NameID = {user}@example.test`, format emailAddress, attributes `email`, `displayName`, `givenName`, `sn`), and `GET /fake-idp/submit.js` (the one-line auto-submit script, so the CSP stays `script-src 'self'`). It also exposes Go-level knobs for negative tests: `SignWithWrongKey`, `OmitSignature`, `DuplicateAssertion`, `Audience`, `Destination`, `Recipient`, `NotOnOrAfter`, `InResponseTo`, `NameIDFormat`, `Status`, `EncryptFor(cert)`, `SessionNotOnOrAfter`. Mounted only by `serve --fake-aws`.
- **Go tests**: `internal/saml` unit tests use the fake IdP to produce responses and assert every row of the validation table (one test per reason code); `internal/api/saml_test.go` drives the HTTP flow with `httptest` and a cookie jar (login → follow redirect to fake IdP → POST to ACS → session cookie → `/api/me`), including cross-task behaviour by running two `api.Server` instances over one store. DB tests need `HM_TEST_DATABASE_URL` as everywhere.
- **devseed**: unchanged by default (SSO stays off so every existing e2e spec keeps working). `fake.MockupEstate` untouched.
- **Playwright** `web/e2e/sso.spec.ts` (flows project, last): as `pavel`, open Settings → Single sign-on, fetch metadata from `http://127.0.0.1:{port}/fake-idp/metadata`, save, run Test sign-in (as `alex`), see "Would create user alex"; **self-link**: sidebar "Link SSO account" → password → fake IdP as `pavel` → toast "Your SSO account is linked.", Users page shows pavel "Password" + "SSO linked"; **pending link**: set a pending link on `dana` to `dana@example.test`, Users shows "Link pending"; make `pavel` break-glass, turn SSO on (confirm); fresh context: SSO sign-in as `dana` → signed in as the existing `dana` (her permissions intact: she sees her clusters), Users shows dana "SSO linked" and still "Password"; fresh context: SSO as `alex` → JIT, overview says "You don't have access to anything yet. Ask a global admin."; as pavel grant alex Viewer on Dev, alex reloads and sees clusters; `omer` (local, not break-glass) gets "Use single sign-on to sign in to this account."; `pavel` password sign-in still works via the "Sign in with a password instead" link (break-glass kept the password); pavel SSO sign-in also works and lands on the same `pavel` account; replaying a captured ACS POST through `page.request` returns a 303 to `/login?error=sso_failed`; **unlink**: unlink `alex` (JIT, no password) → temporary password shown; unlink `dana` → no password shown, she keeps hers; `finally` turns SSO off (dana signs in with her seed password again, proving nothing was cleared), unlinks pavel, removes break-glass and deletes `alex` so later runs start clean. `web/e2e/helpers.ts` `login()` learns to click the link when present. `playwright.config.ts` sets `HM_SESSION_KEY: 'e2e-session-key'`.
- `make run` sets `HM_SESSION_KEY=dev-session-key` so SSO can be configured locally against `http://127.0.0.1:8480/fake-idp/metadata`.

---

## Documentation updates

- `README.md`: new **"Single sign-on (SAML)"** section under Configuration (prerequisites, step-by-step for Okta, Entra ID, Google Workspace, Keycloak with the exact values: ACS URL, entity ID, NameID, "app login URL" for tile clicks, Keycloak "Client signature required: Off"), the recommended rollout order (configure → test → link your own account → flag a break-glass admin → set pending links for existing operators → enable), break-glass explanation, linking and unlinking, passwords are never cleared (what works while SSO is on vs off), recovery, limitations (no SLO, no IdP-initiated, no group mapping); env table gains `HM_SAML_REQUEST_TTL`; First run gains "After SSO is on, `create-admin` creates break-glass admins"; Security section gains the SSO bullets.
- `terraform/modules/harbormaster/README.md`: "Single sign-on" paragraph (no module change; browser-mediated POST; internal ALB fine).
- `docs/design/2026-10-31-harbormaster-design.md` §10: one line pointing here (done with this spec).
- `docs/superpowers/plans/2026-10-31-00-conventions.md`: plan 07 row (done with this spec).
- `THIRD_PARTY_NOTICES.md`: crewjam/saml (BSD-2-Clause), beevik/etree (BSD-2-Clause), russellhaering/goxmldsig (Apache-2.0), mattermost/xml-roundtrip-validator (Apache-2.0), jonboulle/clockwork (Apache-2.0), golang-jwt/jwt v4 (MIT; pulled by the module even though `samlsp` is not imported). `tools/check-notices.sh` requires these strings.

---

## Open questions for the owner

Answered on 2026-11-02 (recorded in "Decisions already made"): default label "Sign in with SSO"; `create-admin` always creates break-glass admins; NameID default `unspecified`; IdP-initiated stays out; linking existing users is in scope (D11); passwords are never cleared; pending links expire after 14 days.

None open.

---

## Decisions log (this spec)

| Decision | Choice | Alternatives | Decided by |
|---|---|---|---|
| Library | crewjam/saml v0.5.1 + goxmldsig v1.6.1 direct | gosaml2; hand-rolled | spec author (no typesafe-ai available) |
| IdP-initiated | not supported; document IdP "app login URL" | allow with short replay window | spec author |
| SLO | out of scope | SP-initiated SLO | spec author |
| Request state and replay cache | Postgres rows; binding cookie as defence in depth | signed cookie only | spec author |
| SP key storage | DB, AES-GCM under HKDF(HM_SESSION_KEY); generated on first save | Secrets Manager via Terraform; plaintext | spec author |
| AuthnRequest signing | off in v1 | on, POST binding | spec author |
| Identity key | (entity ID, NameID); transient rejected | email attribute | spec author |
| Linking by match | never auto-link by username/email; JIT creates a new user with a suffixed username | match by username/email | owner constraint "no silent takeover" |
| Linking existing users | in scope: self-link (password re-entered, CSRF POST, works while SSO disabled) and admin pending link (exact NameID, 14-day expiry); unlink admin-only with temporary password when needed | follow-up only | owner (review 2026-11-02); details spec author |
| Credential model | password and SSO identity are independent columns; break-glass requires a password; every user has at least one credential | `auth_source` enum with "SSO users cannot be break-glass" | spec author (relaxed to let the daily-SSO ops lead hold the break-glass password) |
| Password after link / on enable | never cleared; `sso_required` gates non-break-glass password sign-in while SSO is on; turning SSO off restores it | clear at link time (self-lockout while disabled); clear at enable time | owner (second review), dormant-hash trade-off accepted |
| Pending-link expiry | 14 days | 7 d, 30 d, none | owner (second review) |
| Lockout guard | ≥ 1 break-glass admin (with password) to enable and while enabled; create-admin makes break-glass admins | none; CLI `sso --disable` | spec author |
| Sessions | existing machinery; `not_after` from SessionNotOnOrAfter; SAML sessions end on disable/IdP change | ignore SessionNotOnOrAfter | spec author |
| Config storage | typed `saml_config` single-row table | `settings` key/value | spec author |
| Migration number | `0070_saml.up.sql` | `0061` | existing step-of-ten pattern |
