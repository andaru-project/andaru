# Notifications

Status: proposed, 2026-11-23, for [#116](https://github.com/andaru-project/andaru/issues/116). It builds on the event bus ([design](2026-11-23-event-bus-design.md), #154, #164): a SQLite log with durable consumers that read from a cursor and are told of gaps. The owner's decisions are under "Owner decisions"; the rest is proposed.

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

A clear carries only `key`. Notifications are published durably: a lost notification is the case this system exists to prevent.

**Sources state their notifications when they start.** A source's change and its durable event are written to two stores (the bus's log is its own file), so a crash between the two loses the event. Each source therefore raises what is true, and clears what is not, every time the engine starts: the failed apply, the decisions waiting, the full disk. A raise of an active notification only updates it, so the repeats cost nothing, and a raise lost to a crash comes back on the next start.

## The notification layer

The layer runs in the engine, as the bus's first durable consumer: `Consume("notifications", "notification/#", handler)`.

- **What it keeps.** The active notifications are kept in `notifications.db` in the state directory, keyed by source and key. A raise inserts or updates one; a clear or a dismissal moves it to a bounded history (the last 500, or 30 days); a dismissal records who dismissed it.
- **When it is done with an event.** Its handler returns nil only once the event is committed to `notifications.db` and its delivery jobs are published, so the bus delivers the event again after a crash in between. The event's `id` is stored, with its source, alongside the change it made: one seen before changes nothing again, and its delivery jobs are published again with their same IDs, which the outbound service recognises.
- **Missed notifications.** The bus tells the layer of a gap when events it should have had are gone: they aged out while the layer was behind, the layer's consumer was removed for being away, or the bus's store was replaced. The layer then raises its own notification, "Notifications may have been missed", with severity `warning`, pointing to the engine journal. The gap is done with once that notification is committed.
- **What it reads at start.** The active list comes from `notifications.db`, so a restart shows the same list. The layer attaches its consumer before it serves the list, as the bus's design asks of every consumer.
- **Who may publish.** The bus checks the publish grant; the layer also checks that an event's source matches the topic it came on.
- **Times.** A notification keeps the time its source put in the event, unless that is later than when the layer recorded it: a source cannot keep its notification on top by its clock, and one read late from a backlog keeps the time it happened. A change never dates a notification earlier than it was.
- **What it refuses.** An event it cannot read is logged and skipped rather than retried, since a retry would hold up every notification after it: one whose source does not match its topic, an unknown type or severity, an empty key or subject, a link that is not a path in the web UI, a `requires` that is not a capability, an event with no time or one before 1970, or text with control characters, line or paragraph separators, or invisible format characters other than the zero-width joiner and non-joiner. A link must be a path in the web UI that a browser cannot read as another site (`//host`, `/\host`).

## The API

- **`GET /api/v1/notifications`** (`notifications.read`) lists the active notifications the caller may see, the most recently raised or changed first.
- **`POST /api/v1/notifications/{id}/dismiss`** (`notifications.manage`) dismisses one. Seeing a notification takes `notifications.read` as well as what it requires, and one the caller may not see is not found. If the layer's store did not open, both answer that notifications are unavailable. The dismissal is recorded in the layer's history with the account that made it.
- The built-in read-only role has `notifications.read`; the administrator has both.

## The web UI

- **The header** shows the number of active notifications the account may see. The count is coloured by the highest severity among them.
- **The Notifications page** lists them: subject, severity, source, when, the detail, and the link. Accounts with `notifications.manage` can dismiss one; a dismissed notification comes back if its source raises it again.
- **A dashboard widget** shows the newest few.
- **Live updates.** After each change to its list, the layer publishes a non-durable `notifications/changed` event with no content. The engine adds that topic to the event stream (`GET /api/v1/events`) for accounts with `notifications.read`, and the page reads the list again at each event, as the shell does for the change state. The stream never carries a notification, so only the layer subscribes to `notification/#`, and the list the page reads is filtered per account by the API.

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

- **How a notification is handed over.** For each raise that a channel's filter passes, the layer publishes a delivery job durably on `delivery/<channel>`. The job holds its ID (the notification event's `id` and the channel's), the rendered message, and what the channel needs to send it, credentials included.
- **How it is received.** The outbound service reads its own durable consumer, `deliveries`, on `delivery/#`, through the bus's client. It sends each job and acks it once the channel accepted it or the job is given up. It remembers the IDs of the jobs it sent for a day, so a job published again after a crash is not sent twice.
- **Retries.** A failed send is retried with backoff for up to an hour, then given up and logged. A channel that fails three jobs in a row raises its own notification, `delivery/<channel>`; that notification is not sent to the failing channel.
- **Missed deliveries.** If the bus tells the outbound service of a gap, jobs were lost before they were sent. It raises a notification saying so, which the layer shows and sends like any other.
- **Where the outbound service connects.** It is not root, so it cannot reach `bus.sock`. The bus gets a second socket for Andaru's own unprivileged services, `/run/andaru-services/bus.sock`. A client there is known by its peer user ID, as on the engine's web socket, not by a token: `andaru-outbound`'s user is granted to subscribe to `delivery/#` (and to its #115 topics) and to publish on `notification/delivery` and its #115 results. The bus's options gain this: a listener that names its clients by their peer user ID.
- **Credentials travel with the job.** They are in `bus.db`, which is root's alone in the 0700 state directory, until every consumer is done with the job and the log is pruned. The alternative is for the outbound service to read the secret store, which would give it all the secrets rather than only those it sends with.

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
- **Missed notifications** and **missed deliveries:** above.
- **Disk:** the engine raises `disk/<mount>` when the state directory's file system passes 90% (`warning`) or 97% (`critical`), and clears it below 85%. It checks once a minute.

Each states its notifications when the engine starts, as above.

## What comes first

1. **The layer and its store:** the durable consumer, `notifications.db`, raise, clear and dismiss, the missed-notifications notice, and the capabilities.
2. **The sources above**, each stating its notifications at start.
3. **The web UI:** the count, the page, the widget, and `notifications/changed` in the stream.
4. **The services socket** in the bus, with peer user IDs, and **the outbound service**, shared with #115, then **the channels** one by one: webhook first, since it needs no format of its own, then ntfy, Pushover, Telegram and e-mail.

## Open

- Quiet hours and rate limits per channel: not proposed. A channel's filter is the only control until they are asked for.
