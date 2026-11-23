# The event bus

Status: proposed, 2026-11-23, for [#154](https://github.com/andaru-project/andaru/issues/154). The owner chose MQTT for the bus, with an embedded broker, and asked for it to come before the add-on model. The owner's decisions are under "Owner decisions". A spike on branch `spike/mqtt-bus` tried the broker with a SQLite store.

## The problem

Andaru's processes learn of changes in different ways today: the web UI polls for a new settings head or build, and the engine re-reads what it needs. Add-ons will need the same: a certificate renewed, an alias changed or deleted. pfSense offers such hooks only to some packages; Andaru offers them to everyone allowed to have them.

One bus carries these changes. Andaru's own processes subscribe to it mostly in memory, and add-ons subscribe over a socket. A process reads the current state once when it starts, and from then on events drive it.

## Owner decisions

- **MQTT 5, with an embedded broker.** The broker is [mochi-mqtt](https://github.com/mochi-mqtt/server) (`github.com/mochi-mqtt/server/v2`), running inside the engine. It is not a separate daemon. Add-ons use any MQTT client.
- **Durable means at least once.** A durable event is published with QoS 1. The broker keeps it until each subscriber acknowledges it, across restarts of the broker and of the subscriber. Subscribers must handle duplicates.
- **Non-durable events take the risk.** An event published with QoS 0 is delivered once, if the subscriber is there. It is lost if a process dies, and whoever publishes or subscribes to it accepts that.
- **A change to state that a process reads at start is non-durable.** A certificate changed, firewall rules modified, an alias changed or removed: a subscriber that missed one because a process died re-reads the state when it starts or reconnects anyway. The event is a convenient way to learn of a change while running, not the record of it. Durable events are those whose content cannot be read back from state, such as notifications.
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
- **A Unix socket**, root-owned like the engine's API socket, for other processes: `andaru-web` and, later, add-ons. A client authenticates with the account token system that already exists. A browser does not connect to the broker; `andaru-web` subscribes for it.

Andaru's own built-in subsystems hold grants defined in the code. The engine's own publishes go through mochi's inline client, which bypasses the broker's permission check, so a built-in publisher is checked by the code that calls the bus rather than at the broker.

## Durability, step by step

**Non-durable events.** Published with QoS 0, after the change they report has been committed. Nothing is stored. A subscriber that follows state reads it when it starts and again every time it connects to the broker without a session (the `Session Present` flag of MQTT's CONNACK is off), and subscribes again. Its subscriptions use a clean start, so after any disconnection, including the engine restarting, it re-reads. Events lost while the engine or the subscriber was down are covered by that re-read. A change committed in the engine whose publish is lost is lost only when the engine itself dies, which also disconnects every subscriber.

**Durable events, from a change to the bus.** A durable event is written in the same SQLite transaction as what it reports, to an outbox table in the store that owns it. After the commit, the engine publishes it with QoS 1 and deletes it from the outbox. On start, the engine publishes whatever is left in the outbox first. A crash between the publish and the delete publishes the event twice, which at-least-once allows.

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
2. **The first event.** `config/head`, non-durable, published after each save, and `andaru-web` subscribing to it over the Unix socket, so the web UI learns of a new head or build without polling (which also settles [#150](https://github.com/andaru-project/andaru/issues/150)).
3. **Notifications** ([#116](https://github.com/andaru-project/andaru/issues/116)): the first durable events, with the outbox, and the notification subsystem as their subscriber.
4. **Add-ons:** the grants from manifests, when the add-on model exists.

## Risks

- **mochi-mqtt's last release is v2.7.9, of March 2025.** Andaru depends on its hook interface, its store loading and its session handling. If it stops being usable, the broker can be replaced behind the same topics and payloads, since add-ons speak MQTT, not mochi.
- **It adds dependencies:** `rs/xid` and `gorilla/websocket`, the latter for a listener Andaru does not use.
