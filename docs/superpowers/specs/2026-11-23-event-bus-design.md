# The event bus

Status: steps 1 and 2 built, 2026-11-23, for [#154](https://github.com/andaru-project/andaru/issues/154). The owner's decisions are under "Owner decisions".

The bus was first built on MQTT, with the mochi-mqtt broker embedded in the engine and a SQLite storage hook. The reviews of that build found about twenty ways a durable subscriber could lose events without being told (#165, #167). Nearly all came from the seam between the broker's sessions in memory and their copy in SQLite, and some needed a fork of mochi to close. The owner then chose a bus of Andaru's own, on 2026-11-23: a log in SQLite with cursors, which has no second copy of anything to keep in step.

## The problem

Andaru's processes learn of changes in different ways today. The web UI polls for a new settings head or build, and the engine re-reads what it needs. Add-ons will need the same: a certificate renewed, an alias changed or deleted. pfSense offers such hooks only to some packages; Andaru offers them to everyone allowed to have them.

One bus carries these changes. Andaru's own processes subscribe to it in memory, and other processes subscribe over a socket. A process reads the current state once when it starts, and from then on events drive it.

The web UI follows the bus through the engine: the engine serves the events a browser may see as Server-Sent Events, and `andaru-web` passes the stream through like any other API request.

## Owner decisions

- **A bus of Andaru's own** (2026-11-23), not MQTT.
  - **The store:** a log of events in SQLite, with a cursor per durable consumer.
  - **The wire:** a protocol of JSON lines on a Unix socket, simple enough that a client for an add-on takes a few dozen lines in any language.
  - **Why:** the only boundary between systems is the add-ons, and they reach Andaru through a layer of their own, so a standard protocol buys little.
- **Durable means at least once.** A durable event is in the log once its publish returns. Each durable consumer gets it, and gets it again until the consumer says it is done with it, across restarts of the bus and of the consumer. Consumers must handle duplicates.
- **Non-durable events take the risk.** A non-durable event goes to the subscribers present, once, and is lost if a process dies. Whoever publishes or subscribes to it accepts that.
- **A change to state that a process reads at start is non-durable.** Examples: a certificate changed, firewall rules modified, an alias changed or removed. A subscriber that missed one because a process died re-reads the state when it starts or reconnects anyway. The event is a convenient way to learn of a change while running, not the record of it. Durable events are for content that cannot be read back from state.
- **The publisher chooses, per publish,** whether an event is durable. A notifier may publish its notifications durably or not.
- **Permissions at both ends.** Every topic is gated twice:
  - a publisher may publish only on the topics it is granted for publishing;
  - a subscriber may subscribe, or consume, only on the topics it is granted for subscribing.

  The bus never redacts an event. A subscriber that shows an event to a user filters what that user may see.
- **Notifications are events.** Andaru's own subsystems publish notification events, and so do add-ons granted that permission. Only the notification subsystem may subscribe to them.
- **Add-on grants follow Android's model.**
  - An add-on's manifest requests its topics. The administrator grants or denies each request at install, or at an update that requests a new one.
  - Grants are never added automatically. An update that stops requesting one loses it.
  - A grant changed later takes effect when the add-on restarts, and the change restarts it.
  - Add-ons must work without any grant they requested.

## Topics and payloads

A topic names what changed, from the general to the specific, with `/` between levels: `config/head`, `apply/<id>`, `alias/<id>`, `certificate/<id>`, `notification/<source>`. A filter may use `+` for one level, and `#` as its last level for any number of levels, none included. A grant covers a filter only if it matches everything the filter can match, so `alias/+` does not cover `alias/#`.

The payload is JSON, and the bus refuses anything else. It holds:

- **`id`:** unique per event. A subscriber that sees an `id` again drops the duplicate.
- **`type`:** the event, such as `config.head.changed`. Its name carries a version when its payload changes incompatibly (`…changed.v2`), under the same rules as the settings schema.
- **`time`** and **`source`:** when it happened and who published it.
- **`data`:** the event's own content.

## The bus

The engine opens the bus before it serves its API, and closes it after. The bus is one SQLite file, `bus.db` in the state directory, with three tables:

- **`events`:** the log of durable events, numbered in order (`seq`).
- **`consumers`:** each durable consumer, with its filter and its cursor (the last event it is done with).
- **`expired`:** the names of consumers removed for being away too long.

A consumer can be read in two ways:

- **In the engine (`Consume`).** The engine reads one with a handler: an event is done with once the handler returns nil, and an error retries it after a pause.
- **Over the socket.** A client reads its own consumer. It is sent at most 64 events before it acknowledges, and its acknowledgements are cumulative.
  - A client's consumers are named under its user name, so no client reads another's.
  - A consumer is read by one connection at a time; a new one takes it over.
  - A client that reconnects gets everything after its cursor again.

A new consumer starts at the end of the log, since it reads the state it follows when it starts.

A **subscription** is live and not stored. It gets every event on its filter from the moment it is made, durable or not, and nothing it missed.

**Retention.** The log keeps an event until every consumer is done with it, and for at most the retention period, seven days by default. A consumer nobody reads for the retention period is removed, and its name is kept for ten times as long.

**Gaps.** A consumer that comes back after events it should have had were removed is told so with a gap, before anything else, and reads on from the oldest event kept. This happens when the events aged out, or when the consumer itself was removed. That one signal covers every way a durable consumer can miss an event: the notification subsystem, for one, marks that notifications may have been missed.

**The store.** A store the bus cannot open is moved aside (`bus.db.unreadable-<time>`) and the bus starts with an empty one, rather than stopping the engine. Every consumer is then new, and reads the state it follows.

### The protocol

A client connects to a Unix socket and exchanges one JSON object per line each way.

1. **Hello.** It first sends `{"op":"hello","user":…,"token":…}`.
2. **Requests.** Each request carries an `id`, and is answered with `{"op":"ok","id":…}` or `{"op":"error","id":…,"message":…}`. The requests are:
   - `publish`, with `topic`, `payload` and `durable`. A durable publish is answered once the event is in the log.
   - `subscribe`, with `filter`.
   - `consume`, with `consumer` and `filter`.
3. **Acks.** `{"op":"ack","consumer":…,"seq":…}` says a consumer is done with its events up to `seq`. It gets no answer.
4. **From the bus.** The bus sends:
   - `{"op":"event","topic":…,"payload":…,"seq":…}`, which names the `consumer` when it is for one;
   - `{"op":"gap","consumer":…}`.

A write to a client has ten seconds. A client 256 frames behind is disconnected, so it never holds up a publisher. `internal/bus` has the Go client, `bus.Dial`.

### Sockets

- **`bus.sock`, for Andaru's root processes.** It sits beside the engine's API socket: root-owned, mode 0600, in the engine's runtime directory. Only root reaches it, so a client there is trusted with the grants of the user name it gives. `andaru realize` publishes on it.
- **For add-ons and Andaru's unprivileged services.** They will need a socket of their own, where a client authenticates. It comes with the add-on model and with the outbound service of #115 and #116.

### The web UI

A browser does not connect to the bus.

**The engine's stream.** The engine serves `GET /api/v1/events` on its web socket.

- **Who may open it.** The engine checks the session cookie and `configuration.read`.
- **What it sends.** It subscribes to `config/head` and `apply/+`, and writes each event's payload, compacted, as one Server-Sent Event data line.
- **Heartbeat.** Every 25 seconds it writes a comment and checks the session again. A stream therefore ends within that time of its session ending or losing the capability.
- **Activity.** Neither opening nor keeping the stream counts as activity for the idle timeout.
- **Slow browsers.** A browser that falls 16 events behind loses its stream.

**`andaru-web`.**

- **HTTP/2.** It serves HTTP/2 as well as HTTP/1.1, so a page's stream shares its connection with its requests.
- **Its own connections.** It passes the stream through with a pool of backend connections of its own.
- **Write limit.** Each write of a stream must finish within a minute, so a browser that stops reading loses its stream.
- **Shutdown.** Shutting down `andaru-web` ends its streams at once.

## What is built

1. **The bus** (`internal/bus`), as described above, started by the engine with `bus.sock`. The tests cover:
   - live subscriptions;
   - consumers in the engine and over the socket: order, retries, the window and acknowledgements, a restart, and a takeover of a name;
   - gaps by age and by expiry;
   - pruning;
   - grants at both ends and authentication;
   - an unusable store.

   Removing any of those mechanisms fails a test.
2. **The first events.**
   - **`config/head`.** The configuration store calls `OnSave` with each version committed as the head, at its one commit point. A save, a restore, an upgrade's answers and a migration on start all publish. The type is `config.head.changed`, with `data` `{"version": "<id>"}`.
   - **`apply/<id>`.** The engine publishes it once it has stored a new generation, before realize runs it. `andaru realize` publishes it after each activate, confirm and rollback, the timer's rollback included. The engine passes `--bus-socket` for that, and if the engine is down realize only warns. The type is `apply.changed`, with `data` `{"id": <id>}`.
   - **The web UI** follows both through the engine's stream instead of polling every 15 seconds:
     - It reads the change state when the stream opens and at every event.
     - It reads once when a stream fails.
     - A stream the server refuses is opened again every 15 seconds.
     - A hidden tab holds no stream.
     - While an apply is under way, it reads every 15 seconds: those events are not durable, and a lost one must not hide the confirmation the apply waits for.

## What comes next

3. **Notifications** ([#116](https://github.com/andaru-project/andaru/issues/116), [design](2026-11-23-notifications-design.md)): the notification subsystem as the first durable consumer.
4. **The socket for unprivileged services and add-ons**, with authentication, for the outbound service and, later, the grants from add-on manifests.

## Risks

- **A durable publish and the change it reports are in two files.** A change committed in another store, and then lost with the process before its durable event is written, leaves no event. The first durable publishers (notifications) must write their event in the same step as what they report, or be able to raise it again from state.
- **`andaru-web` holds at most 64 event streams.** A 65th open page waits for a connection and follows no events until it gets one.
