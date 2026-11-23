package bus

import (
	"bytes"
	"database/sql"
	"fmt"
	"sync"

	mqtt "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/hooks/storage"
	"github.com/mochi-mqtt/server/v2/packets"
	_ "modernc.org/sqlite"
)

// store keeps the broker's sessions, subscriptions, undelivered and retained
// messages in SQLite, and the outbox of durable publishes not yet delivered to
// every session.
//
// mochi's hooks return no error, so a failed write for a client discards that
// client's session instead of leaving it to lose a message silently.
type store struct {
	mqtt.HookBase
	db              *sql.DB
	server          *mqtt.Server
	maximumInflight int
	// drop is server.DropSession; tests hold it back.
	drop func(*mqtt.Client, packets.Code) *mqtt.Client
	// discarding holds the clients a discard was asked for, so each is dropped once; drops counts the drops
	// still running, which Stop waits for.
	discarding sync.Map
	drops      sync.WaitGroup
	// stopping is set by Stop, under dropsMu, so no drop starts once Stop waits for them.
	dropsMu  sync.Mutex
	stopping bool
}

func (*store) ID() string { return "store" }

func (*store) Provides(b byte) bool {
	return bytes.Contains([]byte{
		mqtt.OnSessionEstablished, mqtt.OnDisconnect, mqtt.OnSubscribed, mqtt.OnUnsubscribed,
		mqtt.OnWillSent, mqtt.OnQosPublish, mqtt.OnQosComplete, mqtt.OnQosDropped,
		mqtt.OnClientExpired, mqtt.OnPublish, mqtt.OnPublished,
		mqtt.StoredClients, mqtt.StoredInflightMessages, mqtt.StoredSubscriptions,
	}, []byte{b})
}

func (s *store) Init(config any) error {
	path, ok := config.(string)
	if !ok || path == "" {
		return mqtt.ErrInvalidConfigType
	}
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS mqtt_state (
			kind TEXT NOT NULL, key TEXT NOT NULL, value BLOB NOT NULL, PRIMARY KEY (kind, key)) WITHOUT ROWID;
		CREATE TABLE IF NOT EXISTS outbox (
			seq INTEGER PRIMARY KEY AUTOINCREMENT, key TEXT NOT NULL UNIQUE, topic TEXT NOT NULL, payload BLOB NOT NULL)`); err != nil {
		db.Close()
		return err
	}
	// A crash between a discard deleting a client's row and the drop forgetting the rest leaves rows of a
	// session that is gone. mochi would load its subscriptions without a client, so they go here.
	if _, err := db.Exec(`DELETE FROM mqtt_state AS r WHERE r.kind IN (?, ?) AND NOT EXISTS (
		SELECT 1 FROM mqtt_state AS c WHERE c.kind = ? AND c.key = substr(r.key, 1, instr(r.key, ':') - 1))`,
		storage.SubscriptionKey, storage.InflightKey, storage.ClientKey); err != nil {
		db.Close()
		return err
	}
	s.db = db
	return nil
}

func (s *store) Stop() error {
	s.dropsMu.Lock()
	s.stopping = true
	s.dropsMu.Unlock()
	s.drops.Wait()
	return s.db.Close()
}

func (s *store) put(kind, key string, value storage.Serializable) error {
	data, err := value.MarshalBinary()
	if err == nil {
		_, err = s.db.Exec(`INSERT INTO mqtt_state (kind, key, value) VALUES (?, ?, ?)
			ON CONFLICT (kind, key) DO UPDATE SET value = excluded.value`, kind, key, data)
	}
	if err != nil {
		s.Log.Error("bus store write failed", "error", err, "kind", kind, "key", key)
	}
	return err
}

func (s *store) del(kind, key string) error {
	_, err := s.db.Exec(`DELETE FROM mqtt_state WHERE kind = ? AND key = ?`, kind, key)
	if err != nil {
		s.Log.Error("bus store delete failed", "error", err, "kind", kind, "key", key)
	}
	return err
}

// forget deletes a client's session from the store, best effort: the client
// is already gone from the broker.
func (s *store) forget(id string) {
	_ = s.del(storage.ClientKey, id)
	for _, kind := range []string{storage.SubscriptionKey, storage.InflightKey} {
		// The keys of one client are "<id>:…", which sort from "<id>:" up to "<id>;", byte by byte.
		if _, err := s.db.Exec(`DELETE FROM mqtt_state WHERE kind = ? AND key >= ? AND key < ?`, kind, id+":", id+";"); err != nil {
			s.Log.Error("bus store delete failed", "error", err, "kind", kind, "client", id)
		}
	}
}

// current reports whether cl is the broker's live record for its ID, not one
// discarded or taken over by a newer connection.
func (s *store) current(cl *mqtt.Client) bool {
	existing, ok := s.server.Clients.Get(cl.ID)
	return ok && existing == cl
}

// discard drops the session cl holds, or the one a client that took it over holds, so that the client reconnects
// without one and knows it may have missed messages. While cl still holds the session, its row goes first, so a
// restart before the drop finds no session either. A client is dropped once. The drop itself is mochi's DropSession, which takes the lock a takeover holds; mochi
// holds that lock while it runs some hooks, so the drop runs apart from the hook that asks for it. The session's
// other rows are forgotten in OnUnsubscribed, which the drop runs under that lock.
func (s *store) discard(cl *mqtt.Client, why packets.Code) {
	if cl.IsDropped() {
		return
	}
	if _, asked := s.discarding.LoadOrStore(cl, struct{}{}); asked {
		return
	}
	if s.current(cl) {
		_ = s.del(storage.ClientKey, cl.ID)
	}
	s.dropsMu.Lock()
	defer s.dropsMu.Unlock()
	if s.stopping {
		return // the client row is gone; the rest goes when the store opens next
	}
	s.drops.Add(1)
	go func() {
		defer s.drops.Done()
		defer s.discarding.Delete(cl)
		if holder := s.drop(cl, why); holder != nil {
			s.Log.Warn("bus session discarded", "client", holder.ID, "reason", why.Reason)
		}
	}()
}

func message(kind, id string, cl *mqtt.Client, pk packets.Packet, sent int64) *storage.Message {
	props := pk.Properties.Copy(false)
	return &storage.Message{
		ID: id, T: kind, Client: cl.ID, Origin: pk.Origin, PacketID: pk.PacketID,
		FixedHeader: pk.FixedHeader, TopicName: pk.TopicName, Payload: pk.Payload,
		Sent: sent, Created: pk.Created,
		Properties: storage.MessageProperties{
			PayloadFormat: props.PayloadFormat, PayloadFormatFlag: props.PayloadFormatFlag,
			MessageExpiryInterval: props.MessageExpiryInterval, ContentType: props.ContentType,
			ResponseTopic: props.ResponseTopic, CorrelationData: props.CorrelationData,
			SubscriptionIdentifier: props.SubscriptionIdentifier, TopicAlias: props.TopicAlias,
			User: props.User,
		},
	}
}

func inflightKey(cl *mqtt.Client, pk packets.Packet) string { return cl.ID + ":" + pk.FormatID() }

func (s *store) updateClient(cl *mqtt.Client) {
	if _, discarding := s.discarding.Load(cl); discarding || !s.current(cl) {
		return
	}
	props := cl.Properties.Props.Copy(false)
	err := s.put(storage.ClientKey, cl.ID, &storage.Client{
		ID: cl.ID, T: storage.ClientKey, Remote: cl.Net.Remote, Listener: cl.Net.Listener,
		Username: cl.Properties.Username, Clean: cl.Properties.Clean,
		ProtocolVersion: cl.Properties.ProtocolVersion,
		Properties: storage.ClientProperties{
			SessionExpiryInterval: props.SessionExpiryInterval, SessionExpiryIntervalFlag: props.SessionExpiryIntervalFlag,
			AuthenticationMethod: props.AuthenticationMethod, AuthenticationData: props.AuthenticationData,
			RequestProblemInfo: props.RequestProblemInfo, RequestProblemInfoFlag: props.RequestProblemInfoFlag,
			RequestResponseInfo: props.RequestResponseInfo, ReceiveMaximum: props.ReceiveMaximum,
			TopicAliasMaximum: props.TopicAliasMaximum, User: props.User, MaximumPacketSize: props.MaximumPacketSize,
		},
		Will: storage.ClientWill(cl.Properties.Will),
	})
	if err != nil {
		s.discard(cl, packets.ErrImplementationSpecificError)
	}
}

func (s *store) OnSessionEstablished(cl *mqtt.Client, _ packets.Packet) { s.updateClient(cl) }
func (s *store) OnWillSent(cl *mqtt.Client, _ packets.Packet)           { s.updateClient(cl) }

func (s *store) OnDisconnect(cl *mqtt.Client, _ error, expire bool) {
	if expire {
		if s.current(cl) {
			s.forget(cl.ID)
		}
		return
	}
	s.updateClient(cl)
}

func (s *store) OnClientExpired(cl *mqtt.Client) { s.forget(cl.ID) }

// Inline subscriptions are the engine's non-durable ones and are not kept.
// A dropped client's rows are gone; one of its hooks still running does not write new ones.
func (s *store) OnSubscribed(cl *mqtt.Client, pk packets.Packet, reasonCodes []byte) {
	if cl.Net.Inline || cl.IsDropped() {
		return
	}
	for i, f := range pk.Filters {
		if reasonCodes[i] >= packets.ErrUnspecifiedError.Code {
			continue
		}
		key := cl.ID + ":" + f.Filter
		if err := s.put(storage.SubscriptionKey, key, &storage.Subscription{
			ID: key, T: storage.SubscriptionKey, Client: cl.ID, Qos: reasonCodes[i], Filter: f.Filter,
			Identifier: f.Identifier, NoLocal: f.NoLocal, RetainHandling: f.RetainHandling,
			RetainAsPublished: f.RetainAsPublished,
		}); err != nil {
			s.discard(cl, packets.ErrImplementationSpecificError)
			return
		}
	}
}

func (s *store) OnUnsubscribed(cl *mqtt.Client, pk packets.Packet) {
	if cl.Net.Inline {
		return
	}
	// The drop unsubscribes the holder with a packet of its own (packet ID 0), under the session lock: its rows
	// go then. A late UNSUBSCRIBE from the dropped connection itself changes nothing, since a successor may
	// already hold rows under the same ID.
	if cl.IsDropped() {
		if pk.PacketID == 0 {
			s.forget(cl.ID)
		}
		return
	}
	for _, f := range pk.Filters {
		if err := s.del(storage.SubscriptionKey, cl.ID+":"+f.Filter); err != nil {
			s.discard(cl, packets.ErrImplementationSpecificError)
			return
		}
	}
}

// OnQosPublish runs once a message is held for a client and before it is sent.
// The broker drops messages beyond MaximumInflight without telling anyone, so
// a client that reaches it loses its session first.
func (s *store) OnQosPublish(cl *mqtt.Client, pk packets.Packet, sent int64, _ int) {
	if cl.IsDropped() {
		return
	}
	key := inflightKey(cl, pk)
	if err := s.put(storage.InflightKey, key, message(storage.InflightKey, key, cl, pk, sent)); err != nil {
		s.discard(cl, packets.ErrImplementationSpecificError)
		return
	}
	if cl.State.Inflight.Len() >= s.maximumInflight {
		s.discard(cl, packets.ErrQuotaExceeded)
	}
}

// OnQosComplete forgets a delivery once mochi has. mochi removes an acknowledged delivery from the client's
// inflight messages before it calls this, so a packet ID still held there is a delivery waiting for its
// acknowledgement, and the completion is the client's own publish with the same ID (mochi keeps both directions
// by packet ID, mochi-mqtt/server#545): the delivery's row stays. A failed delete only means the message is sent
// again later, which at least once allows.
//
// A client whose session was taken over or dropped completes nothing of the session's: a takeover clears the old
// client's inflight messages after its successor inherited them under the same keys, and a stale connection may
// still acknowledge its own publish with an ID its successor uses. A drop has forgotten the rows already.
func (s *store) OnQosComplete(cl *mqtt.Client, pk packets.Packet) {
	if cl.IsTakenOver() || cl.IsDropped() {
		return
	}
	if _, held := cl.State.Inflight.Get(pk.PacketID); held {
		return
	}
	_ = s.del(storage.InflightKey, inflightKey(cl, pk))
}

func (s *store) OnQosDropped(cl *mqtt.Client, pk packets.Packet) { s.OnQosComplete(cl, pk) }

// outboxKey names a durable publish from a client outside the engine. The
// packet ID is not reused before the broker acknowledges it.
func outboxKey(pk packets.Packet) string {
	return fmt.Sprintf("%s:%d:%d", pk.Origin, pk.PacketID, pk.Created)
}

// OnPublish runs before the broker acknowledges a publish. A durable publish
// from outside the engine is kept in the outbox first, so a crash before it
// reaches every session publishes it again; if it cannot be kept, the
// publisher gets an error instead of the acknowledgement. The engine's own
// publishes are put in the outbox by Bus.Publish.
func (s *store) OnPublish(cl *mqtt.Client, pk packets.Packet) (packets.Packet, error) {
	if cl.Net.Inline || pk.FixedHeader.Qos == 0 {
		return pk, nil
	}
	if err := s.outboxPut(outboxKey(pk), pk.TopicName, pk.Payload); err != nil {
		return pk, packets.ErrImplementationSpecificError
	}
	return pk, nil
}

// OnPublished runs once the publish is held for every subscriber.
func (s *store) OnPublished(cl *mqtt.Client, pk packets.Packet) {
	if cl.Net.Inline || pk.FixedHeader.Qos == 0 {
		return
	}
	_ = s.outboxDelete(outboxKey(pk))
}

func (s *store) outboxPut(key, topic string, payload []byte) error {
	_, err := s.db.Exec(`INSERT INTO outbox (key, topic, payload) VALUES (?, ?, ?)`, key, topic, payload)
	if err != nil {
		s.Log.Error("bus outbox write failed", "error", err, "topic", topic)
	}
	return err
}

func (s *store) outboxDelete(key string) error {
	_, err := s.db.Exec(`DELETE FROM outbox WHERE key = ?`, key)
	if err != nil {
		s.Log.Error("bus outbox delete failed", "error", err, "key", key)
	}
	return err
}

// drain publishes what an earlier run left in the outbox, in order.
func (s *store) drain() error {
	rows, err := s.db.Query(`SELECT key, topic, payload FROM outbox ORDER BY seq`)
	if err != nil {
		return err
	}
	type entry struct {
		key, topic string
		payload    []byte
	}
	var pending []entry
	for rows.Next() {
		var e entry
		if err := rows.Scan(&e.key, &e.topic, &e.payload); err != nil {
			rows.Close()
			return err
		}
		pending = append(pending, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, e := range pending {
		if err := s.server.Publish(e.topic, e.payload, false, 1); err != nil {
			return err
		}
		if err := s.outboxDelete(e.key); err != nil {
			return err
		}
	}
	return nil
}

func all[T any, P interface {
	*T
	storage.Serializable
}](s *store, kind string) ([]T, error) {
	rows, err := s.db.Query(`SELECT value FROM mqtt_state WHERE kind = ? ORDER BY key`, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []T{}
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var v T
		if err := P(&v).UnmarshalBinary(data); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *store) StoredClients() ([]storage.Client, error) {
	return all[storage.Client](s, storage.ClientKey)
}
func (s *store) StoredSubscriptions() ([]storage.Subscription, error) {
	return all[storage.Subscription](s, storage.SubscriptionKey)
}
func (s *store) StoredInflightMessages() ([]storage.Message, error) {
	return all[storage.Message](s, storage.InflightKey)
}
