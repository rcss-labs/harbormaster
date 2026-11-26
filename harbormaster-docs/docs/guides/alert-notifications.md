# Alert notifications

How to send the failure alerts Harbormaster already shows on the Overview to Slack, Telegram, PagerDuty, a webhook or email. For global admins who set up on-call routing.

![Notification settings](../assets/screens/notifications.png)

Notifications are under **Settings > Notifications** (`/settings/notifications`, global admin only). Alert derivation is unchanged: notifications read the same alerts as the Overview.

## What is sent and when

| Message | When |
| --- | --- |
| Opened | A failure alert opens. |
| Update | For a cluster or account entry, when more services fail or the severity rises. Nothing is sent when it gets better. |
| Repeat | While it stays open, at the route's repeat interval (`Do not repeat`, `Every 30 minutes` or `Every 2 hours`). A repeat lists the current services. |
| Resolved | It has been gone for 2 minutes (a shorter flap is one incident), if the route has **Notify when resolved** and its opened message was delivered. |
| Diagnosis | Only on a route with **Investigate with AI** on. An AI investigation of the alert finished with a summary, the alert is still open and the Opened message was delivered. Sent once for each route and alert. Never for PagerDuty. |
| Review | Only on a route that a [scheduled AI review](ai-agent-reviews.md) sends to. A review run finished with a summary. Sent once for each route and run. Not an alert. Never for PagerDuty. |

With **Investigate with AI** on, a route also gets a [Diagnosis](#diagnosis-messages) after the Opened message. See [Automatic AI investigations](ai-agent-investigator.md).

A route can also be chosen by a scheduled review, and then gets a [Review](#review-messages) message when the review finishes. That is set on the review, not on the route. See [Scheduled AI reviews](ai-agent-reviews.md).

A route can choose from seven alert types: service failing, service degraded, restart loop, unhealthy tasks or targets, deployment failed, cannot place tasks, and account unreachable. `info` alerts are never sent.

## Destinations

One route is one channel. For two Slack channels or two Telegram chats, add two routes.

- **Slack**: paste an incoming webhook URL (`https://hooks.slack.com/...`). It posts to the channel the webhook belongs to. The optional **Channel** field is only a label.
- **Telegram**: create a bot with BotFather, add the bot to the chat (for a channel, make it an administrator), then enter the **Bot token** and **Chat ID** (a number, negative for groups and channels, or `@channelname`). For a group topic, enter the **Topic ID**. To find a chat ID, send the bot a message and open `https://api.telegram.org/bot<token>/getUpdates`.
- **PagerDuty**: paste an Events API v2 **integration key** and choose the US or EU region. Opened and updates trigger an incident, resolved resolves it, and the dedup key keeps one incident per alert. PagerDuty gets no diagnosis message. On a route with **Investigate with AI** on, the incident's link opens `/investigations/incident/<incident id>` in Harbormaster, which shows that incident's investigation once it exists.
- **Webhook**: Harbormaster POSTs one JSON object per notification with `event`, `scope`, `severity`, `title`, `text`, `service`, `cluster`, `account_id`, `region` and `url`. Cluster and account entries add `threshold`, `failing`, `total` and `services`. An optional **Signing secret** adds `X-Harbormaster-Signature: sha256=<HMAC of the body>`. `X-Harbormaster-Delivery` is a stable id per delivery. A route with **Investigate with AI** on also receives a `diagnosis` event: see [Diagnosis messages](#diagnosis-messages).
- **Email**: needs `HM_SMTP_HOST` and `HM_SMTP_FROM` (and usually `HM_SMTP_USERNAME` and `HM_SMTP_PASSWORD`) on the server. STARTTLS is required unless `HM_SMTP_TLS=implicit`. Without `HM_SMTP_HOST` the Email option is disabled. Enter up to 10 addresses.

## Scope

**Send alerts from** is a list of entries. An alert goes to the route when any entry takes it. An empty list means every failure, everywhere.

- **Service and tag entries** (an AWS tag `key=value` or a Harbormaster tag) send one notification for each failure alert of those services.
- **Cluster and account entries** (an account for all regions, or one region) carry a number N (default 1) and send one aggregated notification when N or more of their services are failing. It lists up to 10 failing services with their most severe alert. A service counts once however many alerts it has. An unreachable account opens an account entry whatever N is.

Routes are independent. With cluster A holding three services where only service 2 fails, a route on cluster A with N=1 plus a route on service 2 send two notifications. With N=2 on the cluster, only the service route sends one until a second service fails.

An entry for a cluster, service or account that no longer exists is marked `No longer exists` and matches nothing. A route whose entries are all gone shows `Matches nothing`.

**Catch-all.** **Only when no other route matches** makes a route a safety net. It gets an alert only when no other enabled, non-catch-all route notifies about it.

## Scaling is not a failure

A service scaled up from zero is `failing`, and a scale-out is `degraded`, until the new tasks run. The [off-hours](scaling-and-off-hours.md) wake does this every morning. `HM_NOTIFY_SETTLE` (default 3 minutes) makes `service_failing` and `service_degraded` wait that long before they are notified or counted toward a threshold. Restart loops, failed deployments and the other types notify at once. The trade-off: a service that is simply down is reported up to 3 minutes late through those two types. Set it to `0` to notify immediately. A deployment in progress, a service scaled to zero and scaling in raise no alert.

## Diagnosis messages

**Investigate with AI** is a switch on each route, off by default. It appears only while the AI agent is on, and a route cannot be switched on without it. With it on, Harbormaster asks the AI agent to investigate the alert after the Opened message went out, and sends a **Diagnosis** when there is a summary. The Opened message is never held back for it: it is sent on its own schedule whether or not the agent works. For what the investigation does, what it can get wrong and what it costs, see [Automatic AI investigations](ai-agent-investigator.md).

A diagnosis is sent when all of these hold:

- the route is on, has the switch on, and is not PagerDuty;
- the Opened message of that alert was delivered to the route;
- the investigation finished with a summary;
- the alert has not resolved;
- the later of the investigation's end and the Opened message's delivery is within `HM_AGENT_INVESTIGATION_WINDOW` (15 minutes by default) of now. A short window means a slow investigation, or an Opened message held through quiet hours and sent late, can finish too late: the investigation is stored and readable, but no Diagnosis is sent. A longer window sends later diagnoses, but also lets alerts that share an investigation span more time.

If the alert resolves first, the investigation is still stored and readable, but no Diagnosis is sent, and one already waiting is cancelled. A route whose switch is turned off or that is deleted before the send gets nothing. Other routes are unaffected. A Diagnosis for a warning follows the route's quiet hours (warnings are held until 08:00 when **Hold warnings** is on); critical ones are never held. It appears in the delivery log as **Diagnosis**, is retried like other messages, and does not count toward the route's "last three deliveries failed" warning.

Every diagnosis message carries "Written by AI. Check it before acting." ahead of the text and a link to the investigation. The text is the model's summary, cleaned again at send time: a summary that fails the checks is replaced by a fixed sentence, and every link that is not to your Harbormaster address is replaced by `[link removed]`. See [what the summary check does and does not catch](ai-agent-investigator.md#what-the-summary-check-does-and-does-not-catch). The summary is plain text in every destination, with no Markdown.

| Destination | What the diagnosis looks like |
| --- | --- |
| Slack | A message headed `Diagnosis: <alert title>`, the notice line, the summary as plain text (up to 3000 characters), then the place and an **Open the investigation** link. Link previews are off. |
| Telegram | A bold `Diagnosis: <alert title>` headline, the place, the notice line, the summary in a preformatted block (so Telegram makes no mention, command, hashtag or link of it) and an **Open the investigation** link. Link previews are off. |
| Email | Subject `[Harbormaster] Diagnosis: <alert title>`. Plain text: the notice line, the summary and the link. |
| Webhook | A JSON object with `"event": "diagnosis"`, described below. |
| PagerDuty | Nothing is sent. |

### Webhook event

A diagnosis has the fields of every webhook message, a link in `url` to the investigation, and three more: `summary`, `investigation_id` and `ai_generated` (always `true`). `event` is `diagnosis`, not in the `alert.*` family, so a receiver that rejects unknown event names needs a change before you switch the route on.

```json
{
  "event": "diagnosis",
  "delivery_id": 4211,
  "scope": "service",
  "kind": "service_failing",
  "severity": "critical",
  "title": "orders-api is failing",
  "text": "0 of 3 tasks are running.",
  "service": "orders-api",
  "cluster": "production",
  "account_id": "<account-id>",
  "region": "eu-west-1",
  "at": "2026-11-25T09:14:03Z",
  "url": "https://harbormaster.internal.example.com/investigations/87",
  "summary": "The tasks stop a few seconds after they start. The last deployment changed the database host. Check the task's environment and the database security group.",
  "investigation_id": 87,
  "ai_generated": true
}
```

The `title` and `text` are the alert's, as in the Opened message. The signature header, if you set a signing secret, covers this body as for every event. Delivery is at least once: dedupe on `X-Harbormaster-Delivery`.

## Review messages

A [scheduled AI review](ai-agent-reviews.md) names the routes that get its result, 1 to 5 of them, in **Settings > AI agent > Reviews**. A route needs no switch. A review message is not an alert: it has no incident, it is sent once, and it never changes how alerts are sent. A review message is sent when all of these hold:

- the review run finished with a summary;
- the route is on and is not PagerDuty;
- the route is still one of the review's routes, and the review still exists;
- the message is planned within 24 hours of the run finishing.

A review message is planned once for each run and route. If the route is switched off, deleted, or taken off the review before the send, the row is cancelled with a note in the delivery log. Other routes are unaffected. A route is never sent a review that is not on it.

A route's quiet hours apply. With **Hold warnings** on, a review that finishes between 22:00 and 08:00 in the route's time zone is held and sent at 08:00. A review message has the severity `info`, which is held like a warning. It appears in the delivery log as **Review**, is retried like other messages, and does not count toward the route's "last three deliveries failed" warning. Its link opens the result: `/reviews/runs/<id>`.

Every review message carries "Written by AI. Check it before acting." ahead of the text. The text is the model's summary, cleaned again at send time as for a diagnosis: a summary that fails the checks is replaced by a fixed sentence, and every link that is not to your Harbormaster address is replaced by `[link removed]`. See [what the summary check does and does not catch](ai-agent-reviews.md#what-the-summary-check-does-and-does-not-catch). The summary is plain text in every destination.

| Destination | What the review looks like |
| --- | --- |
| Slack | A message headed `<type label> review: <review name>` (for example `Cost review: Weekly spend`), the period, the notice line, the summary as plain text (up to 3000 characters) and an **Open the review** link. Link previews are off. |
| Telegram | A bold `<type label> review: <review name>` headline, the period, the notice line, the summary in a preformatted block (so Telegram makes no mention, command, hashtag or link of it) and an **Open the review** link. Link previews are off. |
| Email | Subject `[Harbormaster] <type label> review: <review name>`. Plain text: the notice line, the period, the summary and the link. |
| Webhook | A JSON object with `"event": "review"`, described below. |
| PagerDuty | Nothing is sent. A review cannot be sent to a PagerDuty route. |

The type labels are Cost, Security findings, Reliability, Capacity and Custom, so a custom review is headed `Custom review: <review name>`. A test run is never sent. The instructions of a custom review are not part of the message. The review name is written by an admin, so it is cleaned to one line and cut.

### Review webhook event

A review has the common fields of a webhook message, with `scope` set to `review` and `severity` to `info`. The alert fields (`text`, `service`, `cluster`, `account_id` and `region`) are empty. `title` is the headline above. `url` links to the result. Six more fields come with it: `summary`, `ai_generated` (always `true`), `review_run_id`, `review_name`, `review_type` (`cost`, `security`, `reliability`, `capacity` or `custom`) and `period`. `event` is `review`, not in the `alert.*` family, so a receiver that rejects unknown event names needs a change before a review sends to it.

```json
{
  "event": "review",
  "delivery_id": 4290,
  "scope": "review",
  "kind": "review",
  "severity": "info",
  "title": "Cost review: Weekly spend",
  "text": "",
  "service": "",
  "cluster": "",
  "account_id": "",
  "region": "",
  "at": "2026-11-25T08:02:41Z",
  "url": "https://harbormaster.internal.example.com/reviews/runs/312",
  "summary": "The estimated run rate is flat at about $4,800 a month. The largest cost is the checkout cluster. Lowering the memory of orders-api is the largest saving.",
  "ai_generated": true,
  "review_run_id": 312,
  "review_name": "Weekly spend",
  "review_type": "cost",
  "period": "Week 48, 2026"
}
```

The signature header, if you set a signing secret, covers this body as for every event. Delivery is at least once: dedupe on `X-Harbormaster-Delivery`.

## Quiet hours, delivery log and retries

- **Hold warnings 22:00–08:00** holds warnings in the route's time zone and sends them at 08:00 if the alert is still open. Critical alerts are never held. A warning that resolves overnight is never sent. [Review messages](#review-messages) are held the same way and sent at 08:00.
- **Send matching alerts** can be `Warning and critical` or `Critical only`.
- The **Delivery log** records every delivery for 30 days (`HM_RETENTION_NOTIFICATION_LOG`). Each is tried three times (at once, after 30 seconds, after 2 minutes, honouring `Retry-After`), then marked `Failed`. You can retry a failed delivery from the log.
- **Send test** sends one message from the task serving the request. Save the route first.
- A route whose last three deliveries all failed shows a warning on Settings.

## Step by step: send failures to Slack

Before you begin: you are a global admin, `HM_SESSION_KEY` is set on the server (the editor says so if not), and the server can reach the internet on 443.

1. In Slack, create an app with an incoming webhook for the channel you want and copy the URL.
2. In Harbormaster open **Settings > Notifications** and select **Add route**.
3. Enter a **Route name**. Set **Send to** to Slack and paste the **Slack webhook URL**. Optionally enter the **Channel** as a label, such as `#alerts`.
4. Under **Send alerts from**, add the clusters, services or tags you care about, or leave it empty for everything.
5. Choose **Send matching alerts**, **Repeat while still open**, the **Alert types**, and **Notify when resolved**.
6. Select **Save route**. A new route is added off; turn on **Route is on** and save again.
7. Select **Send test**.

Check it worked: the test message arrives in Slack and the **Delivery log** shows it as delivered. The editor's message preview shows what real alerts will look like.

## Security and limits

- Secrets (webhook and Slack URLs, routing keys, bot tokens, signing secrets) are sealed in the database under `HM_SESSION_KEY` and are write-only: never returned by the API or written to logs, audit rows or delivery notes. If you rotate `HM_SESSION_KEY`, routes show `Enter the destination again.` and their deliveries fail until you do.
- Destinations must be `https` and are checked when connecting: public addresses only. Loopback, link-local (including the metadata endpoint) and other special ranges are never allowed, redirects are not followed and no proxy is used. To reach an internal receiver, list its private range in `HM_NOTIFY_ALLOWED_PRIVATE_CIDRS`.
- Delivery is at-least-once, so a crash can send one message twice. PagerDuty dedupes by key and webhook receivers can dedupe on `X-Harbormaster-Delivery`.
- At most 50 routes, 100 scope entries per route and 10 email addresses per route.
- Alerts follow the poller's schedule. With idle mode (`HM_POLL_IDLE_INTERVAL`, 5 minutes by default) a night-time alert can be up to that late. `HM_POLL_IDLE_AFTER=0` turns idle mode off.
- With `make run` (fake AWS), demo routes deliver to an in-process receiver at `/fake-notify/received`.

## Next steps

- [Configuration reference](../reference/configuration.md)
- [Automatic AI investigations](ai-agent-investigator.md)
- [Scheduled AI reviews](ai-agent-reviews.md)
- [Overview and clusters](overview-and-clusters.md)
- [Audit log](audit-log.md)
