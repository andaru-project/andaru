package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/andaru-project/andaru/internal/authz"
	"github.com/andaru-project/andaru/internal/bus"
	"github.com/andaru-project/andaru/internal/config"
	"github.com/andaru-project/andaru/internal/delivery"
	"github.com/andaru-project/andaru/internal/notify"
)

type sentJob struct {
	topic string
	job   delivery.Job
}

// jobBus is a bus with what is published on delivery/# read back.
func jobBus(t *testing.T) (*bus.Bus, chan sentJob) {
	t.Helper()
	events, err := bus.Open(bus.Options{Path: filepath.Join(t.TempDir(), "bus.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { events.Close() })
	jobs := make(chan sentJob, 16)
	if _, err = events.Subscribe(delivery.Filter, func(topic string, payload []byte) {
		var e struct {
			Type string
			Data delivery.Job
		}
		if json.Unmarshal(payload, &e) != nil || e.Type != delivery.JobType {
			t.Errorf("not a job: %s", payload)
		}
		jobs <- sentJob{topic, e.Data}
	}); err != nil {
		t.Fatal(err)
	}
	return events, jobs
}

func jobs(t *testing.T, ch chan sentJob) []sentJob {
	t.Helper()
	var got []sentJob
	for {
		select {
		case j := <-ch:
			got = append(got, j)
		case <-time.After(200 * time.Millisecond):
			return got
		}
	}
}

func TestPasses(t *testing.T) {
	for _, row := range []struct {
		min, severity string
		sources       []string
		source        string
		want          bool
	}{
		{"", notify.Info, nil, "apply", true},
		{notify.Info, notify.Info, nil, "apply", true},
		{notify.Warning, notify.Info, nil, "apply", false},
		{notify.Warning, notify.Warning, nil, "apply", true},
		{notify.Warning, notify.Critical, nil, "apply", true},
		{notify.Critical, notify.Warning, nil, "apply", false},
		{notify.Info, notify.Info, []string{"disk", "apply"}, "apply", true},
		{notify.Info, notify.Critical, []string{"disk"}, "apply", false},
	} {
		c := config.NotificationChannel{MinSeverity: row.min, Sources: row.sources}
		if got := passes(c, row.source, row.severity); got != row.want {
			t.Errorf("%+v: %v", row, got)
		}
	}
}

var (
	opsChannel = config.NotificationChannel{ID: "ntf_ops", Name: "ops", Enabled: true, Kind: delivery.Webhook, MinSeverity: notify.Warning,
		URL: "https://hooks.example/T0K3N", Authorization: "Bearer X"}
	plainChannel = config.NotificationChannel{ID: "ntf_plain", Name: "plain", Enabled: true, Kind: delivery.Webhook, URL: "https://hooks.example/plain"}
	offChannel   = config.NotificationChannel{ID: "ntf_off", Name: "off", Kind: delivery.Webhook, URL: "https://hooks.example/off"}
	lostChannel  = config.NotificationChannel{ID: "ntf_lost", Name: "lost", Enabled: true, Kind: delivery.Webhook, Unresolved: true}
)

func someChannels(context.Context) ([]config.NotificationChannel, error) {
	return []config.NotificationChannel{opsChannel, plainChannel, offChannel, lostChannel}, nil
}

// A raise makes one job for each enabled channel that can send and whose filter passes it, with an ID the same event
// makes again.
func TestDeliverNotifications(t *testing.T) {
	events, ch := jobBus(t)
	deliver := deliverNotifications(events, someChannels, "", slog.New(slog.DiscardHandler))
	r := notify.Raise{Key: "apply/3", Severity: notify.Warning, Subject: "Apply 3 failed", Detail: "why", Link: "/applies/3"}
	payload, _ := bus.NewEvent(notify.RaisedType, "engine", r)
	var e bus.Event
	if err := json.Unmarshal(payload, &e); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := deliver(e, r, payload); err != nil {
			t.Fatal(err)
		}
	}
	got := jobs(t, ch)
	if len(got) != 4 || got[0].job.ID != got[2].job.ID || got[1].job.ID != got[3].job.ID {
		t.Fatalf("jobs %+v", got)
	}
	ops, plain := got[0], got[1]
	if ops.job.Channel != opsChannel.ID {
		ops, plain = plain, ops
	}
	want := delivery.Message{Source: "engine", Key: r.Key, Severity: r.Severity, Subject: r.Subject, Detail: r.Detail, Link: r.Link, Raised: e.Time}
	if ops.topic != "delivery/ntf_ops" || ops.job.ID != e.ID+":ntf_ops" || ops.job.Name != "ops" || ops.job.Kind != delivery.Webhook ||
		ops.job.Message != want || string(ops.job.Event) != string(payload) ||
		ops.job.Webhook.URL != opsChannel.URL || len(ops.job.Webhook.Headers) != 1 || ops.job.Webhook.Headers["Authorization"] != "Bearer X" {
		t.Fatalf("ops job %+v", ops)
	}
	if plain.topic != "delivery/ntf_plain" || plain.job.ID != e.ID+":ntf_plain" || plain.job.Webhook.Headers != nil {
		t.Fatalf("plain job %+v", plain)
	}

	// Below the minimum severity, only the channel with none hears of it.
	r.Severity = notify.Info
	if err := deliver(e, r, payload); err != nil {
		t.Fatal(err)
	}
	if got = jobs(t, ch); len(got) != 1 || got[0].job.Channel != plainChannel.ID {
		t.Fatalf("info jobs %+v", got)
	}
}

// A channel's own failure is sent to every other channel but not to it.
func TestChannelFailureNotSentToIt(t *testing.T) {
	events, ch := jobBus(t)
	deliver := deliverNotifications(events, someChannels, "", slog.New(slog.DiscardHandler))
	r := notify.Raise{Key: "channel/ntf_ops", Severity: notify.Warning, Subject: "Channel ops is failing"}
	payload, _ := bus.NewEvent(notify.RaisedType, delivery.Source, r)
	var e bus.Event
	_ = json.Unmarshal(payload, &e)
	if err := deliver(e, r, payload); err != nil {
		t.Fatal(err)
	}
	if got := jobs(t, ch); len(got) != 1 || got[0].job.Channel != plainChannel.ID {
		t.Fatalf("jobs %+v", got)
	}
}

// A channel list that cannot be read retries the raise.
func TestDeliverRetriesUnreadChannels(t *testing.T) {
	events, _ := jobBus(t)
	deliver := deliverNotifications(events, func(context.Context) ([]config.NotificationChannel, error) { return nil, errors.New("busy") }, "", slog.New(slog.DiscardHandler))
	if err := deliver(bus.Event{ID: "x", Source: "s"}, notify.Raise{Severity: notify.Info}, nil); err == nil {
		t.Fatal("no error")
	}
}

// Raises reach the channels through the layer, and a clear does not.
func TestLayerDeliversToChannels(t *testing.T) {
	events, ch := jobBus(t)
	layer, err := notify.Open(filepath.Join(t.TempDir(), "notifications.db"), events, slog.New(slog.DiscardHandler),
		deliverNotifications(events, someChannels, "", slog.New(slog.DiscardHandler)))
	if err != nil {
		t.Fatal(err)
	}
	defer layer.Close()
	if err = notify.Publish(events, "disk", notify.Raise{Key: "var", Severity: notify.Info, Subject: "s"}); err != nil {
		t.Fatal(err)
	}
	if err = notify.Publish(events, "disk", notify.Raise{Key: "var"}); err != nil {
		t.Fatal(err)
	}
	if got := jobs(t, ch); len(got) != 1 || got[0].job.Channel != plainChannel.ID || got[0].job.Message.Key != "var" {
		t.Fatalf("jobs %+v", got)
	}
}

func TestTestChannel(t *testing.T) {
	events, ch := jobBus(t)
	h := newHandlerWithAllServices(nil, http.NotFoundHandler(), nil, nil, nil, nil, nil, nil, notificationsAPI{channels: someChannels, events: events})
	call := func(method, path string, capabilities ...authz.Capability) *httptest.ResponseRecorder {
		t.Helper()
		actor := requestActor{Actor: authz.Actor{Kind: authz.ActorAccount, ID: "00000000-0000-4000-8000-000000000001"},
			AccountID: "00000000-0000-4000-8000-000000000001", Source: "unix", Capabilities: capabilities}
		r := httptest.NewRequest(method, path, nil)
		r = r.WithContext(context.WithValue(r.Context(), authenticatedActorKey{}, actor))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	path := notificationsPath + "/channels/ntf_off/test"
	if w := call(http.MethodPost, path, authz.NotificationsRead); w.Code != http.StatusForbidden {
		t.Fatalf("without notifications.manage: %d", w.Code)
	}
	w := call(http.MethodPost, path, authz.NotificationsManage)
	var body struct{ Job string }
	if w.Code != http.StatusAccepted || json.Unmarshal(w.Body.Bytes(), &body) != nil || !strings.HasPrefix(body.Job, "test:") || !strings.HasSuffix(body.Job, ":ntf_off") {
		t.Fatalf("test: %d %s", w.Code, w.Body)
	}
	got := jobs(t, ch)
	if len(got) != 1 {
		t.Fatalf("jobs %+v", got)
	}
	j := got[0]
	var e bus.Event
	if j.topic != "delivery/ntf_off" || j.job.ID != body.Job || j.job.Name != "off" || j.job.Webhook.URL != offChannel.URL ||
		j.job.Message.Source != "notifications" || j.job.Message.Key != "test" || j.job.Message.Severity != notify.Info ||
		j.job.Message.Subject != "Test notification from Andaru" || !strings.Contains(j.job.Message.Detail, "off") ||
		json.Unmarshal(j.job.Event, &e) != nil || e.Type != notify.RaisedType || e.Source != "notifications" || body.Job != "test:"+e.ID+":ntf_off" {
		t.Fatalf("job %+v", j)
	}
	for _, row := range []struct {
		method, path string
		status       int
	}{
		{http.MethodPost, notificationsPath + "/channels/ntf_none/test", http.StatusNotFound},
		{http.MethodPost, notificationsPath + "/channels//test", http.StatusNotFound},
		{http.MethodPost, notificationsPath + "/channels/ntf_off", http.StatusNotFound},
		{http.MethodPost, notificationsPath + "/channels/ntf_lost/test", http.StatusConflict},
	} {
		if w := call(row.method, row.path, authz.NotificationsManage); w.Code != row.status {
			t.Errorf("%s %s: %d %s", row.method, row.path, w.Code, w.Body)
		}
	}
	if got := jobs(t, ch); len(got) != 0 {
		t.Fatalf("jobs %+v", got)
	}
}

// A job one channel cannot be given keeps no other channel from its job.
func TestOneChannelFailureKeepsTheOthers(t *testing.T) {
	events, ch := jobBus(t)
	broken := plainChannel
	broken.ID = "ntf_#" // no topic can hold it
	list := func(context.Context) ([]config.NotificationChannel, error) {
		return []config.NotificationChannel{broken, opsChannel}, nil
	}
	r := notify.Raise{Key: "k", Severity: notify.Critical, Subject: "s"}
	payload, _ := bus.NewEvent(notify.RaisedType, "engine", r)
	var e bus.Event
	if err := json.Unmarshal(payload, &e); err != nil {
		t.Fatal(err)
	}
	if err := deliverNotifications(events, list, "", slog.New(slog.DiscardHandler))(e, r, payload); err == nil {
		t.Fatal("the broken channel's failure was not reported")
	}
	if got := jobs(t, ch); len(got) != 1 || got[0].job.Channel != opsChannel.ID {
		t.Fatalf("jobs %+v", got)
	}
}

// Each kind of channel's job carries its own target, and no other.
func TestPublishJobTargetsEachKind(t *testing.T) {
	events, ch := jobBus(t)
	for _, row := range []struct {
		c    config.NotificationChannel
		want delivery.Job
	}{
		{config.NotificationChannel{Kind: delivery.Webhook, URL: "https://hook.example/T", Authorization: "Bearer A"},
			delivery.Job{Webhook: &delivery.WebhookTarget{URL: "https://hook.example/T", Headers: map[string]string{"Authorization": "Bearer A"}}}},
		{config.NotificationChannel{Kind: delivery.Ntfy, Server: "https://ntfy.sh", Topic: "alerts", Token: "tk"},
			delivery.Job{Ntfy: &delivery.NtfyTarget{Server: "https://ntfy.sh", Topic: "alerts", Token: "tk"}}},
		{config.NotificationChannel{Kind: delivery.Pushover, Token: "app", User: "usr"},
			delivery.Job{Pushover: &delivery.PushoverTarget{Token: "app", User: "usr"}}},
		{config.NotificationChannel{Kind: delivery.Telegram, Token: "1:bot", ChatID: "-100"},
			delivery.Job{Telegram: &delivery.TelegramTarget{Token: "1:bot", ChatID: "-100"}}},
		{config.NotificationChannel{Kind: delivery.Email, Host: "smtp.example.com", Port: 587, Security: "starttls", Username: "u",
			Password: "pw", From: "a@example.com", To: []string{"ops@example.com"}},
			delivery.Job{Email: &delivery.EmailTarget{Host: "smtp.example.com", Port: 587, Security: "starttls", Username: "u",
				Password: "pw", From: "a@example.com", To: []string{"ops@example.com"}}}},
	} {
		row.c.ID = "ntf_" + row.c.Kind
		if err := publishJob(events, row.c, "", "e:"+row.c.ID, "apply", time.Unix(1, 0), notify.Raise{Key: "k", Severity: notify.Info, Subject: "s"}, []byte(`{}`)); err != nil {
			t.Fatal(err)
		}
		got := jobs(t, ch)
		if len(got) != 1 {
			t.Fatalf("%s: jobs %+v", row.c.Kind, got)
		}
		j := got[0].job
		if j.Kind != row.c.Kind || fmt.Sprint(j.Webhook, j.Ntfy, j.Pushover, j.Telegram, j.Email) != fmt.Sprint(row.want.Webhook, row.want.Ntfy, row.want.Pushover, row.want.Telegram, row.want.Email) {
			t.Errorf("%s: job %+v", row.c.Kind, j)
		}
	}
}

// A job carries the management origin, so a channel can send the notification's link as a full URL.
func TestJobCarriesTheOrigin(t *testing.T) {
	events, ch := jobBus(t)
	list := func(context.Context) ([]config.NotificationChannel, error) { return []config.NotificationChannel{opsChannel}, nil }
	r := notify.Raise{Key: "k", Severity: notify.Critical, Subject: "s", Link: "/system"}
	payload, _ := bus.NewEvent(notify.RaisedType, "engine", r)
	var e bus.Event
	if err := json.Unmarshal(payload, &e); err != nil {
		t.Fatal(err)
	}
	if err := deliverNotifications(events, list, "https://fw.example", slog.New(slog.DiscardHandler))(e, r, payload); err != nil {
		t.Fatal(err)
	}
	if got := jobs(t, ch); len(got) != 1 || got[0].job.Origin != "https://fw.example" {
		t.Fatalf("jobs %+v", got)
	}
}
