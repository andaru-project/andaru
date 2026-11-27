package notify

import (
	"context"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/andaru-project/andaru/internal/authz"
	"github.com/andaru-project/andaru/internal/bus"
)

func TestAddonNotificationEndToEndAndEnforcement(t *testing.T) {
	dir, err := os.MkdirTemp("", "notify-addon")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	sock := filepath.Join(dir, "bus.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	events, err := bus.Open(bus.Options{
		Path:           filepath.Join(dir, "bus.db"),
		TokenListeners: map[string]net.Listener{"addons": l},
	})
	if err != nil {
		l.Close()
		t.Fatal(err)
	}
	defer events.Close()

	changes := make(chan struct{}, 64)
	if _, err := events.Subscribe(ChangedTopic, func(string, []byte) { changes <- struct{}{} }); err != nil {
		t.Fatal(err)
	}
	layer, err := Open(filepath.Join(dir, "notifications.db"), events, slog.New(slog.DiscardHandler), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer layer.Close()

	if err := events.SetClient("addon.sample", "tok-sample", bus.Grants{
		Publish: []string{"notification/addon.sample"},
	}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := bus.Dial(ctx, sock, "addon.sample", "tok-sample")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	// Rejected add-on notifications: forged source, bad links, missing/wrong requires.
	forged, err := bus.NewEvent(RaisedType, "engine", Raise{
		Key: "spoof", Severity: Warning, Subject: "Spoofed", Requires: string(authz.AddonsRead),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Publish(ctx, "notification/addon.sample", forged, true); err != nil {
		t.Fatal(err)
	}

	for _, bad := range []Raise{
		{Key: "no-req", Severity: Warning, Subject: "Missing requires", Link: "/system/addons/sample"},
		{Key: "wrong-req", Severity: Warning, Subject: "Wrong requires", Link: "/system/addons/sample", Requires: string(authz.StatusRead)},
		{Key: "manage-req", Severity: Warning, Subject: "Manage requires", Link: "/system/addons/sample", Requires: string(authz.AddonsManage)},
		{Key: "sibling-link", Severity: Warning, Subject: "Sibling prefix", Link: "/system/addons/sample2", Requires: string(authz.AddonsRead)},
		{Key: "other-addon", Severity: Warning, Subject: "Other addon", Link: "/system/addons/other", Requires: string(authz.AddonsRead)},
		{Key: "traversal", Severity: Warning, Subject: "Traversal", Link: "/system/addons/sample/../other", Requires: string(authz.AddonsRead)},
		{Key: "query-link", Severity: Warning, Subject: "Query", Link: "/system/addons/sample?x=1", Requires: string(authz.AddonsRead)},
		{Key: "core-link", Severity: Warning, Subject: "Core link", Link: "/system/updates", Requires: string(authz.AddonsRead)},
	} {
		topic, payload, err := Event("addon.sample", bad)
		if err != nil {
			t.Fatal(err)
		}
		if err := client.Publish(ctx, topic, payload, true); err != nil {
			t.Fatal(err)
		}
	}

	// Valid add-on notifications: exact /system/addons/<slug>, exact /system/addons, and empty link.
	valid := []Raise{
		{Key: "k1", Severity: Warning, Subject: "Auth expired", Detail: "Re-login", Link: "/system/addons/sample", Requires: string(authz.AddonsRead)},
		{Key: "k2", Severity: Info, Subject: "General", Link: "/system/addons", Requires: string(authz.AddonsRead)},
		{Key: "k3", Severity: Critical, Subject: "No link", Requires: string(authz.AddonsRead)},
	}
	for _, r := range valid {
		topic, payload, err := Event("addon.sample", r)
		if err != nil {
			t.Fatal(err)
		}
		if err := client.Publish(ctx, topic, payload, true); err != nil {
			t.Fatal(err)
		}
		select {
		case <-changes:
		case <-time.After(5 * time.Second):
			t.Fatalf("valid notification %q was not recorded", r.Key)
		}
	}

	active, err := layer.Active()
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 3 {
		t.Fatalf("active notifications = %d (%+v), want 3", len(active), active)
	}

	// Clear works for valid add-on source.
	clearTopic, clearPayload, err := Event("addon.sample", Raise{Key: "k1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Publish(ctx, clearTopic, clearPayload, true); err != nil {
		t.Fatal(err)
	}
	select {
	case <-changes:
	case <-time.After(5 * time.Second):
		t.Fatal("clear notification was not processed")
	}
	active, err = layer.Active()
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 2 {
		t.Fatalf("active notifications after clear = %d, want 2", len(active))
	}
}

func TestValidateRejectsInvalidAddonSourceSlug(t *testing.T) {
	now := time.Now().UTC()
	for _, source := range []string{"addon.", "addon.Bad", "addon.a/b", "addon.1bad"} {
		e := bus.Event{ID: "id1", Type: RaisedType, Time: now, Source: source}
		r := Raise{Key: "k", Severity: Info, Subject: "s", Requires: string(authz.AddonsRead)}
		if err := validate("notification/"+source, e, r); err == nil {
			t.Errorf("validate accepted invalid add-on source %q", source)
		}
		eClear := bus.Event{ID: "id2", Type: ClearedType, Time: now, Source: source}
		if err := validate("notification/"+source, eClear, Raise{Key: "k"}); err == nil {
			t.Errorf("validate accepted clear for invalid add-on source %q", source)
		}
	}
}
