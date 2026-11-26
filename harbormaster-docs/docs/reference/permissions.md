# Permissions reference

The authorization model: global admins, tags, the seven actions, the presets, and how a tag is resolved for a cluster, service or task. It is for admins who grant access and for engineers who need to know exactly what an action allows. For the how-to, see [Users, tags and permissions](../guides/users-tags-permissions.md).

## The model in short

- A **global admin** can do everything, on every tag.
- Everyone else gets **actions on tags**. A user's permission on a tag is a set of actions. There are no per-cluster or per-service grants.
- Every cluster and service has one **effective tag**. What a user may do with it is the set of actions they hold on that tag.
- The server checks permissions on every request, including the WebSocket routes. Hiding a button in the web app is never the only control.
- A resource you cannot see returns `404`, not `403`, so its existence is not revealed.

## Actions

There are seven actions. Each is one bit in the stored permission value.

| Action | Letter | Bit | Allows |
| --- | --- | --- | --- |
| View | V | 1 | See the cluster or service, its tasks, task definitions (including environment variable values), metrics, alerts, timeline, scaling, target health, network and security views. Required for every other action. |
| Logs | L | 2 | Read log history, open a live log tail, and run log searches over the service. Also needed to see error counts from logs on the timeline. |
| Operate | O | 4 | Restart a service (force a new deployment), set the desired count, change auto scaling, stop a task, and wake or sleep an off-hours service. |
| Deploy | D | 8 | Deploy a task definition revision, turn ECS Exec on or off, change deployment settings and the capacity provider strategy, turn the pre-deploy scan gate on or off, request scans and rescans, and set an image's base image. |
| Edit task definition | E | 16 | Register a new task definition revision from an edited body. Registering is not deploying: deploying the result needs Deploy. |
| Terminal | T | 32 | Open an interactive shell in a running task through ECS Exec. |
| Accept risk | R | 64 | Accept a vulnerability or a package's findings on an image, and remove an acceptance. |

Two rules apply to the set a user holds on a tag:

- **Any action implies View.** A set that holds an action but not View is normalised to include View. A grant of `0` means no access.
- **Accept risk is in no preset.** Accepting a vulnerability is a separate responsibility, so you grant it on its own, even to users with the Full preset. A set that includes it shows as Custom.

A check passes when the user holds View on the tag **and** the action itself.

### Mixed changes

A change set (several changes in one request) needs every action its parts need. If any is missing, the whole request is refused before anything is written:

| Part of the change | Action needed |
| --- | --- |
| A new task definition body | Edit |
| Deploying a revision; ECS Exec on or off; deployment configuration; health check grace period; capacity provider strategy; a new task definition body together with a service or scaling change | Deploy |
| Desired count; forced new deployment; auto scaling changes | Operate |

## Presets

The web app offers named bundles. A preset is only a shortcut: the stored value is the set of actions.

| Preset | Actions |
| --- | --- |
| None | No access to the tag. |
| Viewer | View |
| Operator | View, Logs, Operate |
| Deployer | View, Logs, Operate, Deploy, Edit |
| Full | View, Logs, Operate, Deploy, Edit, Terminal |
| Custom | Any other combination, including any set with Accept risk. |

## Global admin

A global admin holds every action on every tag, including Accept risk. Only admins can:

- Manage users, break-glass flags and SSO links, and configure single sign-on.
- Create, edit and delete tags, assign tags to clusters and services, and set permissions.
- Connect, edit, disable and remove AWS accounts.
- Create and edit off-hours schedules and notification routes.
- Read the audit log and change instance settings.
- Add or change task definition fields that can reach the container host (task and execution roles, privileged mode, host volumes and similar). Non-admins may keep or remove these relative to the deployed revision but not add or change them. A rejected draft returns 422 and nothing is registered.

A **break-glass** admin can sign in with a password while single sign-on is on. At least one is required before SSO can be turned on and while it stays on.

## Access tokens are read-only

A request authenticated by an access token is read-only whatever its user's permissions are. It may use only View and Logs, an admin's token included. This check runs before the admin shortcut, and it is a second layer behind the list of routes tokens may call. See [Access tokens](../guides/access-tokens.md).

## Run tokens are read-only

The AI agent calls Harbormaster with a run token (prefix `hma_`), minted for one question as the asking user. The same rule applies as for access tokens: only routes marked as readable by tokens, only View and Logs, an admin's included, checked before the admin shortcut. A run token is tied to one run, ends after `HM_AGENT_RUN_TOKEN_TTL` (15 minutes by default) and ends with the user's sessions. The public MCP gateway refuses it. A run token does not authenticate at all while `HM_AGENT_URL` is unset.

## The AI chat flag

Using the AI chat needs a per-user flag, **AI chat**, set by a global admin in **Settings > Users**. It is off for new users. Global admins can chat without it. The flag grants no data access: the chat sees only what the user's own tags allow. Only global admins can change the flag, the chat, investigation and review models, the allowance, the redaction setting, the [scheduled reviews](#who-reads-a-review) and the [agent identity](#the-agent-identity)'s access. See [AI chat](../guides/ai-agent-chat.md).

## The agent identity

Automatic AI investigations and scheduled AI reviews run as the **agent identity**, a built-in user of a separate kind that is not a person. Its username is `harbormaster-agent` (the first free of that and `harbormaster-agent-1` to `-9`). The prefix `harbormaster-agent` is reserved: no user can be created with a name that starts with it, whatever the case (`_` and `.` count as `-`), and a single sign-on sign-in whose derived name would start with it fails.

- **What it holds.** Only View, and Logs where an admin grants it, on the tags an admin picks in **Settings > AI agent > Agent identity** (**No access**, **View**, **View and logs**). It starts with no access. Every other action is masked off, even if a row for it were stored, and it can never be a global admin.
- **What it can never do.** Sign in by password or single sign-on, hold a session, a personal access token or an OAuth grant, be an admin or a break-glass admin, have the AI chat flag, or use Operate, Deploy, Edit, Terminal or Accept risk. It is not listed on **Settings > Users**, and the user routes answer `404` for it. It cannot be deleted or edited there.
- **How it reads.** For a run it gets a run token, valid for `HM_AGENT_RUN_TOKEN_TTL`, that is read-only like any run token: only the routes tokens may call, only View and Logs. Its use does not count as user activity, so it does not wake AWS polling that was idle.
- **Limits of its own.** `HM_AGENT_IDENTITY_RATE_PER_SECOND` and the two `HM_AGENT_IDENTITY_LOG_SEARCH_*` limits (see [Configuration](configuration.md#ai-agent)).
- **Where it shows.** The delete-tag impact view lists it among those who lose access when a tag it holds is deleted. Changes to its access are audited as `agent_identity_update`.
- **It is off with the agent.** With `HM_AGENT_URL` unset, no identity row is created and nothing in this section applies. Once created, the row stays if you later unset it, inert.

### Who reads an investigation

A person's access to an investigation is checked on every request, from their current permissions:

| Item | Needs |
| --- | --- |
| The summary of a service investigation | View on the service. A service that no longer exists: global admin. |
| The summary of a cluster or account investigation | Global admin, or View on every service listed when it was requested. A list at the 50-service cap, or an empty one: global admin only. |
| One evidence item | View on its service, and Logs as well when it is log text. Hidden items are counted, not described. |
| The list of all investigations in Settings | Global admin. |

Access tokens and run tokens cannot read investigations. See [Automatic AI investigations](../guides/ai-agent-investigator.md#who-sees-what).

### Who reads a review

Scheduled reviews run as the same agent identity. A review reads what the identity may View, with Logs never used, and the identity's access is the only boundary: the scope of a review is a focus, not a permission. Each run stores the tags the identity could View when it started and when it finished (its covered tags). A tag granted and removed again during one run is not covered. A person's access to a review result is checked on every request, from their current permissions:

| Item | Needs |
| --- | --- |
| The summary of a result | Global admin, or View on every covered tag of the run. A run that covered no tag, or a tag that has been deleted since: global admin only. |
| One evidence item | View on its service. A review cites View evidence only, so an item stored with any other action is hidden from everyone, admins included. Hidden items are counted, not described. |
| The list of results (**Reviews**) | Signed in. It holds only the results the person may read. |
| Creating, changing, switching, running or deleting a review, the model for reviews, and the list in **Settings > AI agent** | Global admin. |
| The instructions of a custom review, the copy kept with each run, and a test run | Global admin only, whatever tags the run covered. A reader of a result never receives the instructions. |

Access tokens and run tokens cannot read reviews or manage them, and no tool of the agent reads a review's definition. An unknown or unreadable result answers `404`, never `403`. A test run is hidden from every non-admin, also under the code of the earlier release. See [Scheduled AI reviews](../guides/ai-agent-reviews.md#who-sees-what).

## How tags match clusters, services and tasks

A tag is a Harbormaster object (name, colour and a non-production flag). It is separate from AWS resource tags. Admins assign tags to **clusters** and to **services**.

| Resource | Effective tag |
| --- | --- |
| Cluster | Its assigned tag, or **Dev** when it has none. |
| Service | Its own assigned tag; if none, its cluster's effective tag. |
| Task of a service | The effective tag of the service. |
| Standalone task (no service) | The effective tag of its cluster. |

Notes:

- **Dev** is the built-in default tag. It cannot be deleted or renamed, but you can change its colour and its non-production flag. Anything not tagged explicitly falls back to it.
- **Accounts have no tag.** A user sees an account's resources through the tags of its clusters and services. The accounts and regions a user sees are those where they can see at least one resource.
- Deleting a tag removes its assignments and the permissions granted on it; the affected resources fall back to their next effective tag (the cluster's, then Dev). The tag impact view shows what a change affects before you save.
- A tag marked **Non-production** makes the services under it eligible for the Fargate Spot advisor. Unflagged tags, and services with no tag chosen for them, count as production. The built-in Dev tag starts non-production.
- A tag assigned directly to a service overrides the cluster's tag for that service only.

### Filters never widen access

List routes accept `?tag=` filters, and the web app has account, region and tag filters. They narrow results. They never show a resource the user has no View on.

## Where each action is enforced

| Area | Action |
| --- | --- |
| Lists, detail pages, task definitions, revisions, metrics, alerts, timeline, scaling, target health, security views, analytics, network, storage, config search | View (results are filtered) |
| Log history, log tail, log search, error counts on the timeline | Logs |
| Restart, desired count, auto scaling, stop task, off-hours wake and sleep | Operate |
| Deploy a revision, ECS Exec setting, deployment settings, scan gate, scan requests | Deploy |
| Register a task definition revision | Edit |
| Terminal | Terminal |
| Vulnerability and package acceptances | Accept risk |
| Users, tags, permissions, accounts, audit, SSO, off-hours schedules, notification routes (including the **Investigate with AI** switch), instance settings, the agent identity's access, the list of all investigations, and scheduled AI reviews (their definitions, the model for reviews, running one) | Global admin |

The full route list with required actions is in the [API reference](api.md).

## What a user can see about their own access

`GET /api/me` returns the user's admin flag, their permission value per tag (the bitmask above), the tags they can view, and the accounts and regions they can see. Per-object responses include a `can` block with `view`, `logs`, `operate`, `deploy`, `edit` and `terminal`, which the web app uses to show or hide controls.
