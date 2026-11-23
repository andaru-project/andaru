# Notifications

Status: proposed, 2026-11-23, for [#116](https://github.com/andaru-project/andaru/issues/116). It builds on the event bus ([design](2026-11-23-event-bus-design.md), #154). The owner's decisions are under "Owner decisions"; the rest is proposed and marked where it needs an answer.

## The problem

Some things an administrator must hear about without looking for them: an apply that failed or rolled back, settings waiting for upgrade decisions, a disk filling up, an address alias that keeps failing to refresh (#115). Andaru has no way to tell anyone today. The web UI shows only what a page reads when it is open, and nothing leaves the box.

## Owner decisions

- **A notification is an event on the bus.** Its topic is `notification/<source>`. Andaru's own subsystems publish them (their grant is built in), and so do add-ons that request the permission and are granted it.
- **The notification layer is the only subscriber.** Nothing else can be granted a subscription to notification events. The layer receives every notification whole and filters per user when it shows them: a user sees at most what their permissions allow.
- **Everything that raises a notification is also logged.** The log is the record; the notification is the nudge.
- **A notification clears when its cause does, or when it is dismissed.**
- **The web UI shows them:** a count in the header, a list, and a dashboard widget.
- **Channels are system-wide.** Delivery channels are configured once for the box; there are no per-user channels.
- **Each channel filters what it sends:** by severity, and by event type and source.
- **The channels are e-mail (SMTP), Telegram, Pushover, ntfy and a generic webhook.** The webhook is an HTTP POST of the notification's JSON, which also covers Slack, Discord, Matrix and Gotify.
- **Who sees and manages notifications:** `notifications.read` lets an account see the list and the count; `notifications.manage` lets it configure channels and dismiss notifications. A notification that needs another capability to be seen (an apply needs `configuration.read`) is shown only to accounts that have it as well.
- **The first sources:** a failed or rolled-back apply, settings waiting for upgrade decisions, notifications that may have been missed, and a disk filling up. Alias refreshes (#115) and certificates join when they exist.
- **Fetching and resolving for #115 runs in a separate unprivileged service**, not in the engine, **and the same service sends notifications:** `andaru-outbound`, one process that talks to the outside world (owner, 2026-11-23).
- **A change to the channels takes effect when it is saved,** without an apply (owner, 2026-11-23). Channels change nothing on the network the box runs.

## The notification event

The bus payload is the usual envelope (`id`, `type`, `time`, `source`, `data`). The type is `notification.raised.v1` or `notification.cleared.v1`. For a raise, `data` holds:

- **`key`:** what the notification is about, unique within its source: `apply/12`, `disk/var`, `alias/cloudflare`. A raise with a key that is already active updates that notification instead of adding one, and a clear removes it.
- **`severity`:** `info`, `warning` or `critical`.
- **`subject`:** one line, shown in the list and as a message title.
- **`detail`:** optional, a few lines of plain text.
- **`link`:** optional, a path in the web UI where it can be acted on, such as `/changes`.
- **`requires`:** optional, the capability an account needs, besides `notifications.read`, to see it.

A clear carries only `key`. Notifications are published durably (QoS 1): a lost notification is the case this system exists to prevent.

## The notification layer

The layer runs in the engine. It is the bus's first durable subscriber: an MQTT client over the engine's in-process listener (`Bus.Dial`), with a persistent session subscribed to `notification/#`.

- **What it keeps.** The active notifications are kept in `notifications.db` in the state directory, keyed by source and key. A raise inserts or updates one, a clear or a dismissal moves it to a bounded history (the last 500, or 30 days), and a dismissal records who dismissed it.
- **When it acknowledges.** It acknowledges an event only after the event is committed to `notifications.db` and handed to delivery, so a crash in between delivers the event again. A raise is idempotent by key, and a repeated event is recognised by its `id`.
- **Missed notifications.** When the layer connects and finds no session (`Session Present` off), events may have been lost: a store failure, the in-flight limit, or a session that expired. It raises its own notification, "Notifications may have been missed", with severity `warning`. The notification links to the log.
- **What it reads at start.** The active list comes from `notifications.db`, so a restart shows the same list.
- **Who may publish.** The bus checks the publish grant; the layer also checks that an event's source matches the topic it came on.

## The web UI

- **The header** shows the number of active notifications the account may see. The count is coloured by the highest severity among them.
- **The Notifications page** lists them: subject, severity, source, when, the detail, and the link. Accounts with `notifications.manage` can dismiss one; a dismissed notification comes back if its source raises it again.
- **A dashboard widget** shows the newest few.
- **Live updates.** The engine adds `notification/#` to the event stream (`GET /api/v1/events`). Each event the stream sends is filtered per account by `notifications.read` and `requires`. The page reads the list again at each event, as the shell does for the change state.

## Delivery

### Channels are settings

The channels are a collection of the saved settings (`notifications.channels`), each with:

- **an ID and a name;**
- **its kind and its parameters:**
  - e-mail: server, port, TLS mode (`implicit` or `starttls`), user name, sender and recipients;
  - Telegram: the chat ID;
  - Pushover: the user key;
  - ntfy: the server and the topic;
  - webhook: the URL;
- **its credentials, as references to the secret store:** the SMTP password, the Telegram bot token, the Pushover application token, an optional ntfy token, and optional webhook headers;
- **its filter:** a minimum severity, and optional lists of sources and types. An empty list means all.

They are versioned, backed up and restored like every other setting, and take effect when saved, without an apply.

### The outbound service

Sending needs the network, and the engine has none (`RestrictAddressFamilies=AF_UNIX AF_NETLINK`, no capabilities). The unprivileged service that #115 adds for fetching and resolving, `andaru-outbound`, also sends notifications: one process that talks to the outside world, with no privilege and nothing else.

- **How a notification is handed over.** For each raise that a channel's filter passes, the layer publishes a delivery job durably on `delivery/<channel>`. The job holds the rendered message and what the channel needs to send it, credentials included.
- **How it is received.** The outbound service subscribes to `delivery/#` with a persistent session. It sends the job, and acknowledges it once the channel accepted it or the job is given up.
- **Retries.** A failed send is retried with backoff for up to an hour, then given up and logged. A channel that fails three jobs in a row raises its own notification, `delivery/<channel>`; that notification is not sent to the failing channel.
- **Where the outbound service connects.** It is not root, so it cannot reach `bus.sock`. The bus gets a second socket for Andaru's own unprivileged services, at `/run/andaru-services/bus.sock`. Its peer is checked by user ID, as the web socket's is, and `andaru-outbound` connects there with the grants for its user name: subscribe to `delivery/#` (and to its #115 topics), and publish its delivery results and #115 results.
- **Credentials travel with the job.** They sit in `bus.db`, which is root's alone in the 0700 state directory, until the job is acknowledged. The alternative is for the outbound service to read the secret store, which would give it all the secrets rather than only those it sends with.

### What each channel sends

- **E-mail:** the subject as the subject, the detail and the link (as a full URL to the management origin) as plain text.
- **Telegram:** the subject in bold, the detail and the link, through the Bot API's `sendMessage`.
- **Pushover:** the title and message. A critical notification is sent at Pushover priority 1. Emergency priority, which repeats until acknowledged, is not used.
- **ntfy:** the title, message, priority by severity, and the link as the click action.
- **Webhook:** a POST of `{"id", "type", "time", "source", "data"}`, the bus payload itself, as JSON.

Every channel gets a "Send a test" action on its settings page. The test goes through the same delivery path.

## The first sources

- **Applies:** the engine raises `apply/<id>` when an apply fails or is rolled back, and clears it when a later apply is confirmed. This is `warning`, or `critical` for an automatic rollback.
- **Upgrade decisions:** raised while the saved settings wait for decisions (safe mode), and cleared when they are answered. This is `warning`, linking to `/system/upgrade`.
- **Missed notifications:** above.
- **Disk:** the engine raises `disk/<mount>` when the state directory's file system passes 90% (`warning`) or 97% (`critical`), and clears it below 85%. It checks once a minute.

## What comes first

1. **#165** (bus silent loss) — done in the branch before this one.
2. **The layer and its store:** the durable subscriber, `notifications.db`, raise, clear and dismiss, the missed-notifications notice, and the capabilities.
3. **The sources above.**
4. **The web UI:** the count, the page, the widget, and the stream.
5. **The outbound service and the services socket**, shared with #115, and **the channels**, one by one: webhook first, since it needs no format of its own, then ntfy, Pushover, Telegram and e-mail.

## Open

- Quiet hours and rate limits per channel: not proposed. A channel's filter is the only control until they are asked for.
