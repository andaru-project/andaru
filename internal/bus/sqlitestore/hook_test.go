package sqlitestore

import (
	"bytes"
	"context"
	"database/sql"
	"log/slog"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/eclipse/paho.golang/paho"
	mqtt "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/listeners"
	"github.com/mochi-mqtt/server/v2/packets"
)

// acl grants publishing only on "config/#" and subscribing only to "config/#" and its subtopics.
type acl struct{ mqtt.HookBase }

func (*acl) ID() string { return "acl" }
func (*acl) Provides(b byte) bool {
	return b == mqtt.OnConnectAuthenticate || b == mqtt.OnACLCheck
}
func (*acl) OnConnectAuthenticate(*mqtt.Client, packets.Packet) bool { return true }
func (*acl) OnACLCheck(_ *mqtt.Client, topic string, _ bool) bool {
	return topic == "config/#" || len(topic) > 7 && topic[:7] == "config/"
}

func start(t *testing.T, path string) (*mqtt.Server, string) {
	t.Helper()
	server := mqtt.New(&mqtt.Options{InlineClient: true, Logger: slog.New(slog.NewTextHandler(testWriter{t}, &slog.HandlerOptions{Level: slog.LevelWarn}))})
	if err := server.AddHook(new(acl), nil); err != nil {
		t.Fatal(err)
	}
	if err := server.AddHook(new(Hook), &Options{Path: path}); err != nil {
		t.Fatal(err)
	}
	listener := listeners.NewTCP(listeners.Config{ID: "t", Address: "127.0.0.1:0"})
	if err := server.AddListener(listener); err != nil {
		t.Fatal(err)
	}
	if err := server.Serve(); err != nil {
		t.Fatal(err)
	}
	return server, listener.Address()
}

type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Log(string(bytes.TrimSpace(p)))
	return len(p), nil
}

// connect opens a persistent session; ack false leaves every received message unacknowledged.
func connect(t *testing.T, address string, ack bool) (*paho.Client, chan *paho.Publish) {
	t.Helper()
	conn, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	received := make(chan *paho.Publish, 16)
	client := paho.NewClient(paho.ClientConfig{
		Conn:                       conn,
		EnableManualAcknowledgment: true,
		OnPublishReceived: []func(paho.PublishReceived) (bool, error){func(p paho.PublishReceived) (bool, error) {
			received <- p.Packet
			if ack {
				return true, p.Client.Ack(p.Packet)
			}
			return true, nil
		}},
	})
	expiry := uint32(3600)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := client.Connect(ctx, &paho.Connect{ClientID: "web", KeepAlive: 30, CleanStart: false,
		Properties: &paho.ConnectProperties{SessionExpiryInterval: &expiry}}); err != nil {
		t.Fatal(err)
	}
	return client, received
}

func receive(t *testing.T, received chan *paho.Publish) *paho.Publish {
	t.Helper()
	select {
	case p := <-received:
		return p
	case <-time.After(5 * time.Second):
		t.Fatal("no message")
		return nil
	}
}

func inflightRows(t *testing.T, path string) int {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM mqtt_state WHERE kind = 'IFM'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A message for a subscriber that is offline is stored, survives a broker
// restart, and is delivered when the subscriber reconnects.
func TestOfflineSubscriberGetsMessageAfterRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bus.db")
	server, address := start(t, path)

	client, _ := connect(t, address, true)
	if _, err := client.Subscribe(context.Background(), &paho.Subscribe{Subscriptions: []paho.SubscribeOptions{{Topic: "config/#", QoS: 1}}}); err != nil {
		t.Fatal(err)
	}
	client.Disconnect(&paho.Disconnect{})
	time.Sleep(100 * time.Millisecond)

	if err := server.Publish("config/head", []byte("v42"), false, 1); err != nil {
		t.Fatal(err)
	}
	if n := inflightRows(t, path); n != 1 {
		t.Fatalf("in-flight rows before restart = %d, want 1", n)
	}
	server.Close()

	server, address = start(t, path)
	defer server.Close()
	client, received := connect(t, address, true)
	defer client.Disconnect(&paho.Disconnect{})
	if p := receive(t, received); p.Topic != "config/head" || string(p.Payload) != "v42" {
		t.Fatalf("got %s %q", p.Topic, p.Payload)
	}
	time.Sleep(100 * time.Millisecond)
	if n := inflightRows(t, path); n != 0 {
		t.Fatalf("in-flight rows after ack = %d, want 0", n)
	}
}

// A message delivered but not acknowledged before the broker restarts is delivered again.
func TestUnacknowledgedMessageIsRedeliveredAfterRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bus.db")
	server, address := start(t, path)

	client, received := connect(t, address, false)
	if _, err := client.Subscribe(context.Background(), &paho.Subscribe{Subscriptions: []paho.SubscribeOptions{{Topic: "config/#", QoS: 1}}}); err != nil {
		t.Fatal(err)
	}
	if err := server.Publish("config/head", []byte("v43"), false, 1); err != nil {
		t.Fatal(err)
	}
	receive(t, received)
	client.Disconnect(&paho.Disconnect{})
	server.Close()

	server, address = start(t, path)
	defer server.Close()
	client, received = connect(t, address, true)
	defer client.Disconnect(&paho.Disconnect{})
	if p := receive(t, received); string(p.Payload) != "v43" {
		t.Fatalf("got %q", p.Payload)
	}
}

// Subscribing outside the grant is refused at subscription time.
func TestSubscriptionOutsideGrantIsRefused(t *testing.T) {
	server, address := start(t, filepath.Join(t.TempDir(), "bus.db"))
	defer server.Close()
	client, _ := connect(t, address, true)
	defer client.Disconnect(&paho.Disconnect{})
	suback, err := client.Subscribe(context.Background(), &paho.Subscribe{Subscriptions: []paho.SubscribeOptions{{Topic: "notify/#", QoS: 1}}})
	if err == nil && suback.Reasons[0] < 0x80 {
		t.Fatalf("subscription to notify/# accepted: %v", suback.Reasons)
	}
}
