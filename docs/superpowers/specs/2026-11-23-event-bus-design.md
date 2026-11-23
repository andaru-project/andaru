# The event bus

Status: steps 1 and 2 built, 2026-11-23, for [#154](https://github.com/andaru-project/andaru/issues/154). The owner chose MQTT for the bus, with an embedded broker, and asked for it to come before the add-on model. The owner's decisions are under "Owner decisions". A spike on branch `spike/mqtt-bus` tried the broker with a SQLite store.

## The problem

Andaru's processes learn of changes in different ways today: the web UI polls for a new settings head or build, and the engine re-reads what it needs. Add-ons will need the same: a certificate renewed, an alias changed or deleted. pfSense offers such hooks only to some packages; Andaru offers them to everyone allowed to have them.

One bus carries these changes. Andaru's own processes subscribe to it mostly in memory, and add-ons subscribe over a socket. A process reads the current state once when it starts, and from then on events drive it.

The web UI follows the bus through the engine: the engine serves the events a browser may see as Server-Sent Events, and `andaru-web` passes the stream through like any other API request. A browser never speaks MQTT.

## Owner decisions

- **MQTT 5, with an embedded broker.** The broker is [mochi-mqtt](https://github.com/mochi-mqtt/server) (`github.com/mochi-mqtt/server/v2`), running inside the engine. It is not a separate daemon. Add-ons use any MQTT client.
- **Durable means at least once.** A durable event is published with QoS 1. The broker keeps it until each subscriber acknowledges it, across restarts of the broker and of the subscriber. Subscribers must handle duplicates.
- **Non-durable events take the risk.** An event published with QoS 0 is delivered once, if the subscriber is there. It is lost if a process dies, and whoever publishes or subscribes to it accepts that.
- **A change to state that a process reads at start is non-durable.** A certificate changed, firewall rules modified, an alias changed or removed: a subscriber that missed one because a process died re-reads the state when it starts or reconnects anyway. The event is a convenient way to learn of a change while running, not the record of it. Durable events are for content that cannot be read back from state.
- **The publisher chooses, per publish.** Durability is the QoS the publisher sends with, not a property of the topic. A notifier may publish its notifications durably or not.
- **The store is SQLite.** The broker keeps its sessions, subscriptions and undelivered messages in SQLite, through a storage hook Andaru writes. It does not use the Badger, Pebble, Bolt or Redis hooks that mochi ships.
- **Permissions at both ends.** Every topic is gated twice. A publisher may publish only on the topics it is granted for publishing; a subscriber may subscribe only to the topics it is granted for subscribing. The bus never redacts an event; a subscriber that shows an event to a user filters what that user may see.
- **Notifications are events.** Andaru's own subsystems publish notification events, and so do add-ons granted that permission. Only the notification subsystem may subscribe to them.
- **Add-on grants follow Android's model.** An add-on's manifest requests its topics; the administrator grants or denies each request at install, or at an update that requests a new one. Grants are never added automatically. An update that stops requesting one loses it. A grant changed later takes effect when the add-on restarts, and the change restarts it. Add-ons must work without any grant they requested.

## Topics

A topic names what changed, from the general to the specific, in MQTT's `/` form: `config/head`, `alias/<id>`, `certificate/<id>`, `notification/<source>`. Subscribers bind with MQTT's wildcards: `+` for one level, `#` for the rest.

The payload is JSON:

- **`id`:** unique per event. A subscriber that sees an `id` again drops the duplicate.
- **`type`:** the event, such as `config.head.changed`, with a version in its name when its payload changes incompatibly (`…changed.v2`), under the same rules as the settings schema.
- **`time`** and **`source`:** when it happened and who published it.
- **`data`:** the event's own content.

## The broker in the engine

The engine starts the broker before it serves its API and stops it after. The broker has three ways in:

- **In-memory connections**, for Andaru's own durable subscribers in the engine, such as the notification subsystem. They connect as ordinary MQTT clients over an in-process listener, so they get acknowledgements and the store like any other client.
- **Inline subscriptions**, mochi's direct callbacks, for Andaru's own non-durable subscribers in the engine. There is no acknowledgement and nothing is stored; they are QoS 0.
- **A Unix socket**, `bus.sock` beside the engine's API socket, root-owned with mode 0600 in the engine's runtime directory. Only root reaches it, so a client there is trusted with the grants of the user name it gives. `andaru realize` publishes on it. Add-ons will need a socket of their own, where a client authenticates with the account token system; it comes with the add-on model.

A browser does not connect to the broker. The engine serves `GET /api/v1/events` on its web socket: it checks the session cookie and `configuration.read`, subscribes inline to `config/head` and `apply/+`, and writes each event's payload, compacted, as one Server-Sent Event data line; a payload that is not JSON is dropped, since its line breaks could end the event or add fields to it. Every 25 seconds it writes a comment and checks the session again, so a stream ends within that time of its session ending or losing the capability; neither opening nor keeping the stream counts as activity for the idle timeout. A browser that falls 16 events behind loses its stream, so a publisher never waits for one. `andaru-web` serves HTTP/2 (and HTTP/1.1), so a page's stream shares its connection with its requests. It passes the stream through with a backend connection pool of its own, so open pages never hold up API requests. Instead of its write timeout, each write of a stream must finish within a minute (over HTTP/2, a stream not written to for a minute ends too, which the engine's heartbeat prevents), so a browser that stops reading loses the stream and frees its connection to the engine; a shutdown of `andaru-web` ends its streams at once.

Andaru's own built-in subsystems hold grants defined in the code. The engine's own publishes go through mochi's inline client, which bypasses the broker's permission check, so a built-in publisher is checked by the code that calls the bus rather than at the broker.

## Durability, step by step

**Non-durable events.** Published with QoS 0, after the change they report has been committed. Nothing is stored. A subscriber that follows state reads it when it starts and again every time it connects to the broker without a session (the `Session Present` flag of MQTT's CONNACK is off), and subscribes again. Its subscriptions use a clean start, so after any disconnection, including the engine restarting, it re-reads. Events lost while the engine or the subscriber was down are covered by that re-read. A change committed in the engine whose publish is lost is lost only when the engine itself dies, which also disconnects every subscriber.

**Durable events, from a change to the bus.** A durable event is written in the same SQLite transaction as what it reports, to an outbox table in the store that owns it. After the commit, the engine publishes it with QoS 1 and deletes it from the outbox. On start, the engine publishes whatever is left in the outbox first. A crash between the publish and the delete publishes the event twice, which at-least-once allows.

**Durable events from another process.** An add-on (or `andaru-web`) publishes with QoS 1 and its MQTT client resends until the broker acknowledges with PUBACK. mochi sends that PUBACK before it stores the message for any subscriber, so an engine crash in between would lose an event the publisher believes was taken. Andaru's `OnPublish` hook, which runs before the PUBACK and may refuse the publish, writes a durable publish to the bus's own outbox and returns an error if it cannot; the PUBACK then carries that error and the client publishes again. The outbox is drained as above. What an add-on has not yet handed to the broker is the add-on's own concern.

**Durable events, from the bus to a subscriber.** A durable subscriber keeps a persistent session. The storage hook writes every message the broker holds for it before the broker tries to send it, and deletes it when the subscriber acknowledges. After a restart, the broker loads the sessions and their messages and sends them again.

**When the bus cannot keep its promise.** Three cases lose a durable message for a subscriber:

- **A store write fails.** mochi's hooks return no error, so the broker would carry on with the message held only in memory.
- **A subscriber falls too far behind.** mochi holds at most `maximum_inflight` messages per client (8192 by default) and drops the rest with only a log line.
- **A session expires.** A subscriber away longer than its session expiry loses its session and everything in it.

In all three, Andaru discards the subscriber's session instead: a failed write drops it in memory at once, a subscriber at its limit has its session dropped before the next message would be refused, and an expired session is gone anyway. A durable subscriber that connects and finds no session has missed something, and recovers as its kind of event allows: the notification subsystem, for one, marks that notifications may have been missed. That one signal covers every gap.

One case remains: a failed write followed by an engine restart before the subscriber reconnects. The session reloads from the store without the message that failed, and the subscriber cannot tell. A failed write means the disk is full or failing, and the configuration store fails with it; this is accepted.

**Sessions.** The broker caps the session expiry a client may ask for (`maximum_session_expiry_interval`), so a removed add-on does not leave a queue behind forever. Add-ons are removed with their sessions.

## What comes first

1. **The broker and its store.** `internal/bus`: the broker in the engine, the SQLite storage hook from the spike, the in-memory listener, the session discard rules, and the permission hook with built-in grants. Tests for restart redelivery, the session discard in each of the three cases, and grants at both ends.
2. **The first events.** `config/head`, non-durable, published after each save, and `apply/<id>`, published by `andaru realize` after each activate, confirm and rollback, the timer's rollback included. The web UI follows both through the engine's event stream instead of polling the change state every 15 seconds.
3. **Notifications** ([#116](https://github.com/andaru-project/andaru/issues/116)): the notification subsystem as their subscriber, durable itself, and the outbox for the first durable publishes.
4. **Add-ons:** the grants from manifests, when the add-on model exists.

## What is built

Step 1 is `internal/bus`, which the engine starts:

- **`Open`** starts the broker with the SQLite store, an in-memory listener for the engine's own clients (`Dial`) and the listeners it is given, then publishes what the outbox holds.
- **`Publish(topic, payload, durable)`** and **`Subscribe`** (inline, non-durable, ended by the function it returns) are the engine's side. Inline subscriptions are not stored.
- **Grants** are topic filters per MQTT user name, checked on connect, publish, subscribe and delivery. A grant covers a filter only if it matches everything the filter can match, so `alias/+` does not cover `alias/#`. Clients on other listeners also pass `Authenticate`; the engine's own do not. Only MQTT 5 clients connect, since older ones cannot be told that a durable publish was refused.
- **The outbox is the bus's own** for now. The engine's durable publishes are written to it before they are published; the transaction shared with the change they report comes with the first store that publishes durably.
- **A session belongs to its user.** A client's ID is its user name, or the user name, a slash and more (`realize/<pid>`), so no client takes another user's queued messages; no ID holds a colon. A client may not set a Will, which the broker would publish past the grants, and gets no outbound topic aliases, so what the store holds for it always names its topic. A held message is kept as long as its session: mochi's default drops one a day old.
- **A session is discarded with mochi's `DropSession`** (from Andaru's fork, below), which runs under the lock mochi takes over a session with, and follows only a takeover that carried the session: a client reconnecting at that moment either starts without a session or has the one it took over dropped, never keeps an incomplete one, and a session that was already dropped or replaced by a clean start is left alone. The client's row goes at once, so a restart finds no session; its other rows go under the drop's lock. A client is discarded once, and the bus waits for discards still running when it closes.
- **A client's publish cannot cost a delivery with the same packet ID.** mochi keeps a client's unacknowledged messages of both directions in one map by packet ID (mochi-mqtt/server#545). The store keeps a delivery's row while mochi still holds the delivery, so the acknowledgement of the client's own publish with that ID does not delete it, and the bus offers at most QoS 1, since at QoS 2 mochi could overwrite the delivery itself.
- **A write to a client has ten seconds** (`ClientNetWriteTimeout`): a client that stops reading cannot hold up the publishers, or the bus when it closes.
- **Each row records the connection that wrote it.** A client ID outlives its connections, and a connection's late hook (a completion, a discard's early delete, a write-back undone) deletes only the rows it wrote, never its successor's under the same key.
- **Rows left without their client are removed when the store opens:** a crash between a discard deleting a client's row and the drop forgetting the rest would otherwise bring back its subscriptions under no session.
- **Nothing is retained.** An event reports a change, and one missed is read from state, so the broker offers no retained messages and the store keeps none.
- **A store the bus cannot open is moved aside** (`bus.db.unreadable-<time>`) and the bus starts with an empty one, rather than stopping the engine. Every durable subscriber then finds its session gone, the signal that it may have missed events.
- **The store does not keep mochi's `$SYS` counters**: mochi updates them every second, and writing them would wear the disk for nothing.

The tests run real MQTT clients (`eclipse/paho.golang`, a test dependency) against the broker and restart it on the same file. They cover redelivery to an offline subscriber and of an unacknowledged message, non-durable events not being kept, the outbox on both paths, grants at every check, the engine's in-memory clients, and the session discard at the in-flight limit and on a failed write. Removing any of those mechanisms fails a test.

Step 2:

- **`config/head`.** The configuration store calls `OnSave` with each version committed as the head, at its one commit point, so a save, a restore, an upgrade's answers and a migration on start all publish. The payload's `data` is `{"version": "<id>"}`, type `config.head.changed`.
- **`apply/<id>`.** The engine publishes it once it has stored a new generation, before realize runs it, so other pages show the apply at once. It runs every `andaru realize` verb on a generation with `--bus-socket`, including the rollback its timer runs while no request waits. After the verb, whether it succeeded or not, realize connects with paho, publishes `{"id": <id>}` of type `apply.changed` with QoS 0, and disconnects; if the engine is down it only warns. Boot runs without the flag: the engine starts after it, and its subscribers read the state when they connect.
- **The web UI** opens the stream when the account may read the configuration, reads the change state when the stream opens and at every event, and leaves a dropped stream to the browser to reopen. A hidden tab closes its stream, and opens it again, reading the state, when it is shown. When a stream fails, the change state is read once, until a stream opens again, so a lost session or connection shows as a poll would show it. A stream the server refuses (the session ended, the engine is down) is opened again every 15 seconds. While an apply is activating or awaits confirmation, the change state is also read every 15 seconds: its events are not durable, and a lost one must not hide the confirmation the apply waits for.

## Risks

- **mochi is built from Andaru's fork until a tagged release has what it needs.** `go.mod` replaces it with the `andaru` branch of [andaru-project/mochi-server](https://github.com/andaru-project/mochi-server): v2.8.0 before its release (mochi-mqtt/server#543), plus two fixes offered upstream: `Server.DropSession` with the session lock, `IsDropped` and the takeover changes (mochi-mqtt/server#563), and a delivery queued at the client's receive maximum staying in flight once sent instead of being lost on reconnect. The replace goes when a tagged release carries both.
- **`andaru-web` holds at most 64 event streams.** The pool that `andaru-web` keeps for streams allows 64 connections to the engine; a 65th visible page waits for one and follows no events until it gets one.

- **mochi-mqtt's last release is v2.7.9, of March 2025.** Andaru depends on its hook interface, its store loading and its session handling. If it stops being usable, the broker can be replaced behind the same topics and payloads, since add-ons speak MQTT, not mochi.
- **It adds dependencies:** `rs/xid` and `gorilla/websocket`, the latter for a listener Andaru does not use, and `eclipse/paho.golang` for `andaru realize`.
