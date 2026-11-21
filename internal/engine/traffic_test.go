package engine

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/andaru-project/andaru/internal/host"
)

func TestTrafficHistoryKeepsAnHourPerInterface(t *testing.T) {
	history := &trafficHistory{samples: map[string][]trafficSample{}}
	start := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	at := func(tick int) time.Time { return start.Add(time.Duration(tick) * trafficInterval) }
	bytes := func(n uint64) host.Counters { return host.Counters{RxBytes: n} }

	// ens19 appears late and ens20 goes away early.
	const n = trafficSamples
	for tick := range n + 10 {
		counters := map[string]host.Counters{"ens18": bytes(uint64(tick))}
		if tick >= n+5 {
			counters["ens19"] = bytes(uint64(tick))
		}
		if tick < 20 {
			counters["ens20"] = bytes(uint64(tick))
		}
		history.record(at(tick), counters)
	}
	view := history.view(time.Time{})
	if view.IntervalSeconds != 5 || n != 720 || len(view.Interfaces) != 3 {
		t.Fatalf("%d readings every %d seconds; %+v", n, view.IntervalSeconds, view.Interfaces)
	}
	byName := map[string][]trafficSample{}
	names := []string{}
	for _, entry := range view.Interfaces {
		byName[entry.Name] = entry.Samples
		names = append(names, entry.Name)
	}
	if strings.Join(names, ",") != "ens18,ens19,ens20" {
		t.Fatalf("interfaces are not by name: %v", names)
	}
	// The last hour of readings, oldest first.
	if samples := byName["ens18"]; len(samples) != n || samples[0].RxBytes != 10 || samples[n-1].RxBytes != uint64(n+9) || !samples[0].At.Equal(at(10)) {
		t.Fatalf("ens18 holds %d samples from %v", len(samples), samples[0])
	}
	if samples := byName["ens19"]; len(samples) != 5 || samples[0].RxBytes != uint64(n+5) {
		t.Fatalf("a new interface holds %d samples", len(samples))
	}
	// An interface that has gone keeps what is not yet an hour old: ticks 10 to 19 at the last tick.
	if samples := byName["ens20"]; len(samples) != 10 || samples[0].RxBytes != 10 {
		t.Fatalf("a gone interface holds %d samples from %+v", len(samples), samples[0])
	}

	// A client that holds the history up to a reading is sent what came after it, and every interface's name.
	later := history.view(at(n + 6))
	if got := later.Interfaces; len(got) != 3 || len(got[0].Samples) != 3 || got[0].Samples[0].RxBytes != uint64(n+7) || len(got[1].Samples) != 3 || got[2].Name != "ens20" || got[2].Samples == nil || len(got[2].Samples) != 0 {
		t.Fatalf("since the reading of tick %d: %+v", n+6, got)
	}
	if got := history.view(at(n + 9)).Interfaces[0].Samples; len(got) != 0 {
		t.Fatalf("a client that holds everything was sent %d readings", len(got))
	}
	if got := history.view(at(0).Add(-time.Hour)).Interfaces[0].Samples; len(got) != n {
		t.Fatalf("a client that holds nothing recent was sent %d readings", len(got))
	}

	// And then nothing: it leaves the history when its last reading ages out.
	for tick := n + 10; tick < n+21; tick++ {
		history.record(at(tick), map[string]host.Counters{"ens18": bytes(uint64(tick))})
	}
	if view = history.view(time.Time{}); len(view.Interfaces) != 2 {
		t.Fatalf("an interface gone for an hour is still there: %+v", view.Interfaces)
	}
	// More readings than fit, all within the hour: the newest stay.
	crowded := &trafficHistory{samples: map[string][]trafficSample{}}
	for tick := range n + 280 {
		crowded.record(start.Add(time.Duration(tick)*time.Second), map[string]host.Counters{"ens18": bytes(uint64(tick))})
	}
	if samples := crowded.view(time.Time{}).Interfaces[0].Samples; len(samples) != n || samples[0].RxBytes != 280 {
		t.Fatalf("a crowded hour holds %d samples from %+v", len(samples), samples[0])
	}

	// What is handed out is a copy.
	view.Interfaces[0].Samples[0].RxBytes = 0
	if history.view(time.Time{}).Interfaces[0].Samples[0].RxBytes == 0 {
		t.Fatal("a reader changed the history")
	}
}

// The usage and the traffic history as the schema promises them.
func TestUsageAndTrafficStatus(t *testing.T) {
	root := t.TempDir()
	systemRoot = root
	previous := traffic
	traffic = &trafficHistory{samples: map[string][]trafficSample{}}
	t.Cleanup(func() { systemRoot, traffic = "", previous })
	h := newHandler(nil, http.NotFoundHandler())
	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, rootRequest(http.MethodGet, path))
		return rec
	}

	if rec := get("/api/v1/status/usage"); rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "INVENTORY_UNAVAILABLE") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	// The least a box reports, then one with everything: both as the schema says.
	writeUnder(t, root, "/proc/stat", "cpu  1 0 1 8 0 0 0 0\n")
	rec := get("/api/v1/status/usage")
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	validResponse(t, "UsageResponse", rec.Body.Bytes())
	writeUnder(t, root, "/proc/loadavg", "0.5 0.3 0.1 1/2 3\n")
	writeUnder(t, root, "/proc/meminfo", "MemTotal: 2048 kB\nMemAvailable: 1024 kB\n")
	writeUnder(t, root, "/proc/sys/net/netfilter/nf_conntrack_count", "5\n")
	writeUnder(t, root, "/proc/sys/net/netfilter/nf_conntrack_max", "100\n")
	writeUnder(t, root, "/sys/class/hwmon/hwmon0/name", "coretemp\n")
	writeUnder(t, root, "/sys/class/hwmon/hwmon0/temp1_input", "45500\n")
	rec = get("/api/v1/status/usage")
	validResponse(t, "UsageResponse", rec.Body.Bytes())
	if !strings.Contains(rec.Body.String(), `"connections":{"count":5,"limit":100}`) || !strings.Contains(rec.Body.String(), `"celsius":45.5`) {
		t.Fatalf("%s", rec.Body)
	}

	// An engine that has read nothing yet has an empty history, not none.
	rec = get("/api/v1/status/traffic")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"interfaces":[]`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	validResponse(t, "TrafficResponse", rec.Body.Bytes())
	traffic.record(time.Now(), map[string]host.Counters{"ens18": {RxBytes: 1000}})
	rec = get("/api/v1/status/traffic")
	validResponse(t, "TrafficResponse", rec.Body.Bytes())
	if !strings.Contains(rec.Body.String(), `"name":"ens18"`) || !strings.Contains(rec.Body.String(), `"rxBytes":1000`) {
		t.Fatalf("%s", rec.Body)
	}

	// since: what came after a reading; a time in the form the readings carry, and nothing else in the query.
	if rec = get("/api/v1/status/traffic?since=2999-01-01T00:00:00Z"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"name":"ens18","samples":[]`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	validResponse(t, "TrafficResponse", rec.Body.Bytes())
	if rec = get("/api/v1/status/traffic?since=2000-01-01T00:00:00.123456789Z"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"rxBytes":1000`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	for _, query := range []string{"since=yesterday", "since=", "since=2026-11-21", "limit=5", "since=2000-01-01T00:00:00Z&x=1"} {
		if rec = get("/api/v1/status/traffic?" + query); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "INVALID_REQUEST") {
			t.Fatalf("?%s: %d %s", query, rec.Code, rec.Body)
		}
	}
}

// A clock set back must not leave readings from the future in the history: samples run oldest first.
func TestTrafficHistorySurvivesAClockSetBack(t *testing.T) {
	history := &trafficHistory{samples: map[string][]trafficSample{}}
	wrong := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	right := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	for tick := range 5 {
		history.record(wrong.Add(time.Duration(tick)*trafficInterval), map[string]host.Counters{"ens18": {RxBytes: uint64(tick)}, "veth1": {RxBytes: 1}})
	}
	for tick := range 3 {
		history.record(right.Add(time.Duration(tick)*trafficInterval), map[string]host.Counters{"ens18": {RxBytes: uint64(100 + tick)}})
	}
	view := history.view(time.Time{})
	if len(view.Interfaces) != 1 || view.Interfaces[0].Name != "ens18" {
		t.Fatalf("readings from the future stayed: %+v", view.Interfaces)
	}
	samples := view.Interfaces[0].Samples
	if len(samples) != 3 || samples[0].RxBytes != 100 || !samples[0].At.Equal(right) || !samples[2].At.After(samples[1].At) {
		t.Fatalf("%+v", samples)
	}
}

// The sampler reads at once, so a dashboard opened right after the engine starts has a first point, and it
// stops with its context.
func TestSampleTrafficReadsAtOnceAndStops(t *testing.T) {
	root := t.TempDir()
	writeUnder(t, root, "/sys/class/net/ens18/statistics/rx_bytes", "1000\n")
	for _, counter := range []string{"tx_bytes", "rx_packets", "tx_packets", "rx_errors", "tx_errors", "rx_dropped", "tx_dropped"} {
		writeUnder(t, root, "/sys/class/net/ens18/statistics/"+counter, "0\n")
	}
	history := &trafficHistory{samples: map[string][]trafficSample{}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); sampleTraffic(ctx, history, root) }()
	for deadline := time.Now().Add(5 * time.Second); len(history.view(time.Time{}).Interfaces) == 0; time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the sampler read nothing")
		}
	}
	if samples := history.view(time.Time{}).Interfaces[0].Samples; len(samples) != 1 || samples[0].RxBytes != 1000 {
		t.Fatalf("%+v", samples)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the sampler did not stop with its context")
	}
}
