// Package sqlitestore is a spike: a mochi-mqtt storage hook that keeps the
// broker's sessions, subscriptions, in-flight and retained messages in SQLite.
package sqlitestore

import (
	"bytes"
	"database/sql"
	"errors"

	mqtt "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/hooks/storage"
	"github.com/mochi-mqtt/server/v2/packets"
	"github.com/mochi-mqtt/server/v2/system"
	_ "modernc.org/sqlite"
)

type Options struct{ Path string }

type Hook struct {
	mqtt.HookBase
	db *sql.DB
}

func (h *Hook) ID() string { return "sqlite" }

func (h *Hook) Provides(b byte) bool {
	return bytes.Contains([]byte{
		mqtt.OnSessionEstablished, mqtt.OnDisconnect, mqtt.OnSubscribed, mqtt.OnUnsubscribed,
		mqtt.OnRetainMessage, mqtt.OnWillSent, mqtt.OnQosPublish, mqtt.OnQosComplete, mqtt.OnQosDropped,
		mqtt.OnSysInfoTick, mqtt.OnClientExpired, mqtt.OnRetainedExpired,
		mqtt.StoredClients, mqtt.StoredInflightMessages, mqtt.StoredRetainedMessages,
		mqtt.StoredSubscriptions, mqtt.StoredSysInfo,
	}, []byte{b})
}

func (h *Hook) Init(config any) error {
	options, ok := config.(*Options)
	if !ok || options.Path == "" {
		return mqtt.ErrInvalidConfigType
	}
	db, err := sql.Open("sqlite", "file:"+options.Path+"?_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS mqtt_state (
		kind TEXT NOT NULL, key TEXT NOT NULL, value BLOB NOT NULL, PRIMARY KEY (kind, key)) WITHOUT ROWID`); err != nil {
		db.Close()
		return err
	}
	h.db = db
	return nil
}

func (h *Hook) Stop() error { return h.db.Close() }

// The hook interface returns no errors: a failed write is only logged, and the
// broker goes on as if it had been stored.
func (h *Hook) put(kind, key string, value storage.Serializable) {
	data, err := value.MarshalBinary()
	if err == nil {
		_, err = h.db.Exec(`INSERT INTO mqtt_state (kind, key, value) VALUES (?, ?, ?)
			ON CONFLICT (kind, key) DO UPDATE SET value = excluded.value`, kind, key, data)
	}
	if err != nil {
		h.Log.Error("store write failed", "error", err, "kind", kind, "key", key)
	}
}

func (h *Hook) del(kind, key string) {
	if _, err := h.db.Exec(`DELETE FROM mqtt_state WHERE kind = ? AND key = ?`, kind, key); err != nil {
		h.Log.Error("store delete failed", "error", err, "kind", kind, "key", key)
	}
}

func (h *Hook) delPrefix(kind, prefix string) {
	if _, err := h.db.Exec(`DELETE FROM mqtt_state WHERE kind = ? AND substr(key, 1, ?) = ?`, kind, len(prefix), prefix); err != nil {
		h.Log.Error("store delete failed", "error", err, "kind", kind, "prefix", prefix)
	}
}

func all[T any, P interface {
	*T
	storage.Serializable
}](h *Hook, kind string) ([]T, error) {
	rows, err := h.db.Query(`SELECT value FROM mqtt_state WHERE kind = ? ORDER BY key`, kind)
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

func (h *Hook) updateClient(cl *mqtt.Client) {
	props := cl.Properties.Props.Copy(false)
	h.put(storage.ClientKey, cl.ID, &storage.Client{
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
}

func (h *Hook) OnSessionEstablished(cl *mqtt.Client, _ packets.Packet) { h.updateClient(cl) }
func (h *Hook) OnWillSent(cl *mqtt.Client, _ packets.Packet)           { h.updateClient(cl) }

func (h *Hook) OnDisconnect(cl *mqtt.Client, _ error, expire bool) {
	h.updateClient(cl)
	if expire && !errors.Is(cl.StopCause(), packets.ErrSessionTakenOver) {
		h.OnClientExpired(cl)
	}
}

// An expired client takes its subscriptions and in-flight messages with it;
// the badger hook leaves them behind.
func (h *Hook) OnClientExpired(cl *mqtt.Client) {
	h.del(storage.ClientKey, cl.ID)
	h.delPrefix(storage.SubscriptionKey, cl.ID+":")
	h.delPrefix(storage.InflightKey, cl.ID+":")
}

func (h *Hook) OnSubscribed(cl *mqtt.Client, pk packets.Packet, reasonCodes []byte) {
	for i, f := range pk.Filters {
		key := cl.ID + ":" + f.Filter
		h.put(storage.SubscriptionKey, key, &storage.Subscription{
			ID: key, T: storage.SubscriptionKey, Client: cl.ID, Qos: reasonCodes[i], Filter: f.Filter,
			Identifier: f.Identifier, NoLocal: f.NoLocal, RetainHandling: f.RetainHandling,
			RetainAsPublished: f.RetainAsPublished,
		})
	}
}

func (h *Hook) OnUnsubscribed(cl *mqtt.Client, pk packets.Packet) {
	for _, f := range pk.Filters {
		h.del(storage.SubscriptionKey, cl.ID+":"+f.Filter)
	}
}

func (h *Hook) OnRetainMessage(cl *mqtt.Client, pk packets.Packet, r int64) {
	if r == -1 {
		h.del(storage.RetainedKey, pk.TopicName)
		return
	}
	h.put(storage.RetainedKey, pk.TopicName, message(storage.RetainedKey, pk.TopicName, cl, pk, 0))
}

func (h *Hook) OnRetainedExpired(topic string) { h.del(storage.RetainedKey, topic) }

func (h *Hook) OnQosPublish(cl *mqtt.Client, pk packets.Packet, sent int64, _ int) {
	key := inflightKey(cl, pk)
	h.put(storage.InflightKey, key, message(storage.InflightKey, key, cl, pk, sent))
}

func (h *Hook) OnQosComplete(cl *mqtt.Client, pk packets.Packet) {
	h.del(storage.InflightKey, inflightKey(cl, pk))
}

func (h *Hook) OnQosDropped(cl *mqtt.Client, pk packets.Packet) { h.OnQosComplete(cl, pk) }

func (h *Hook) OnSysInfoTick(sys *system.Info) {
	h.put(storage.SysInfoKey, storage.SysInfoKey, &storage.SystemInfo{ID: storage.SysInfoKey, T: storage.SysInfoKey, Info: *sys.Clone()})
}

func (h *Hook) StoredClients() ([]storage.Client, error) {
	return all[storage.Client](h, storage.ClientKey)
}
func (h *Hook) StoredSubscriptions() ([]storage.Subscription, error) {
	return all[storage.Subscription](h, storage.SubscriptionKey)
}
func (h *Hook) StoredInflightMessages() ([]storage.Message, error) {
	return all[storage.Message](h, storage.InflightKey)
}
func (h *Hook) StoredRetainedMessages() ([]storage.Message, error) {
	return all[storage.Message](h, storage.RetainedKey)
}

func (h *Hook) StoredSysInfo() (storage.SystemInfo, error) {
	v, err := all[storage.SystemInfo](h, storage.SysInfoKey)
	if err != nil || len(v) == 0 {
		return storage.SystemInfo{}, err
	}
	return v[0], nil
}
