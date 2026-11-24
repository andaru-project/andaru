package engine

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/andaru-project/andaru/internal/authz"
	"github.com/andaru-project/andaru/internal/bus"
	"github.com/andaru-project/andaru/internal/notify"
)

func TestNotificationsAPI(t *testing.T) {
	dir := t.TempDir()
	events, err := bus.Open(bus.Options{Path: filepath.Join(dir, "bus.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer events.Close()
	layer, err := notify.Open(filepath.Join(dir, "notifications.db"), events, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	defer layer.Close()
	for _, r := range []notify.Raise{
		{Key: "var", Severity: notify.Warning, Subject: "The disk is filling up"},
		{Key: "apply/3", Severity: notify.Critical, Subject: "Apply 3 rolled back", Requires: string(authz.ConfigurationRead)},
	} {
		if err = notify.Publish(events, "test", r); err != nil {
			t.Fatal(err)
		}
	}
	for start := time.Now(); ; time.Sleep(10 * time.Millisecond) {
		if list, _ := layer.Active(); len(list) == 2 {
			break
		}
		if time.Since(start) > 5*time.Second {
			t.Fatal("the notifications were not recorded")
		}
	}
	h := newHandlerWithServices(nil, http.NotFoundHandler(), nil, nil, nil, nil, notificationsAPI{layer: layer})
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
	keys := func(w *httptest.ResponseRecorder) []string {
		t.Helper()
		var body struct{ Items []notify.Notification }
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &body) != nil {
			t.Fatalf("list: %d %s", w.Code, w.Body)
		}
		var keys []string
		for _, n := range body.Items {
			keys = append(keys, n.Key)
		}
		slices.Sort(keys)
		return keys
	}
	if got := keys(call(http.MethodGet, notificationsPath, authz.NotificationsRead)); !slices.Equal(got, []string{"var"}) {
		t.Fatalf("without configuration.read: %q", got)
	}
	if got := keys(call(http.MethodGet, notificationsPath, authz.NotificationsRead, authz.ConfigurationRead)); !slices.Equal(got, []string{"apply/3", "var"}) {
		t.Fatalf("with configuration.read: %q", got)
	}
	if w := call(http.MethodGet, notificationsPath, authz.ConfigurationRead); w.Code != http.StatusForbidden {
		t.Fatalf("without notifications.read: %d", w.Code)
	}
	list, _ := layer.Active()
	ids := map[string]int64{}
	for _, n := range list {
		ids[n.Key] = n.ID
	}
	dismiss := func(key string) string { return notificationsPath + "/" + strconv.FormatInt(ids[key], 10) + "/dismiss" }
	if w := call(http.MethodPost, dismiss("var"), authz.NotificationsRead); w.Code != http.StatusForbidden {
		t.Fatalf("dismissed without notifications.manage: %d", w.Code)
	}
	if w := call(http.MethodPost, dismiss("apply/3"), authz.NotificationsRead, authz.NotificationsManage); w.Code != http.StatusNotFound {
		t.Fatalf("dismissed one it may not see: %d", w.Code)
	}
	if w := call(http.MethodPost, dismiss("var"), authz.NotificationsManage); w.Code != http.StatusNotFound {
		t.Fatalf("dismissed without notifications.read: %d", w.Code)
	}
	for _, path := range []string{notificationsPath + "/0/dismiss", notificationsPath + "/01/dismiss", notificationsPath + "/x/dismiss", notificationsPath + "/999/dismiss"} {
		if w := call(http.MethodPost, path, authz.NotificationsRead, authz.NotificationsManage); w.Code != http.StatusNotFound {
			t.Fatalf("%s: %d", path, w.Code)
		}
	}
	if w := call(http.MethodPost, dismiss("var"), authz.NotificationsRead, authz.NotificationsManage); w.Code != http.StatusNoContent {
		t.Fatalf("dismiss: %d %s", w.Code, w.Body)
	}
	if got := keys(call(http.MethodGet, notificationsPath, authz.NotificationsRead, authz.ConfigurationRead)); !slices.Equal(got, []string{"apply/3"}) {
		t.Fatalf("after the dismissal: %q", got)
	}
}

// Without a layer, which did not open, the API says notifications are unavailable rather than not there.
func TestNotificationsUnavailable(t *testing.T) {
	h := newHandlerWithServices(nil, http.NotFoundHandler(), nil, nil, nil, nil, notificationsAPI{})
	actor := requestActor{Actor: authz.Actor{Kind: authz.ActorAccount, ID: "00000000-0000-4000-8000-000000000001"},
		AccountID: "00000000-0000-4000-8000-000000000001", Source: "unix", Capabilities: []authz.Capability{authz.NotificationsRead}}
	r := httptest.NewRequest(http.MethodGet, notificationsPath, nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), authenticatedActorKey{}, actor)))
	assertAPIError(t, w, http.StatusServiceUnavailable, "NOTIFICATIONS_UNAVAILABLE")
}

func TestStreamFiltersFollowCapabilities(t *testing.T) {
	for _, row := range []struct {
		capabilities []authz.Capability
		want         []string
	}{
		{nil, nil},
		{[]authz.Capability{authz.ConfigurationRead}, []string{"config/head", "apply/+"}},
		{[]authz.Capability{authz.NotificationsRead}, []string{notify.ChangedTopic}},
		{[]authz.Capability{authz.ConfigurationRead, authz.NotificationsRead}, []string{"config/head", "apply/+", notify.ChangedTopic}},
	} {
		if got := streamFilters(requestActor{Capabilities: row.capabilities}); !slices.Equal(got, row.want) {
			t.Fatalf("%v: %q, want %q", row.capabilities, got, row.want)
		}
	}
}
