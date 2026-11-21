package engine

import (
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
	for tick := range 130 {
		counters := map[string]host.Counters{"ens18": bytes(uint64(tick))}
		if tick >= 125 {
			counters["ens19"] = bytes(uint64(tick))
		}
		if tick < 20 {
			counters["ens20"] = bytes(uint64(tick))
		}
		history.record(at(tick), counters)
	}
	view := history.view()
	if view.IntervalSeconds != 30 || len(view.Interfaces) != 3 {
		t.Fatalf("%+v", view)
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
	// The last 120 readings, oldest first: the hour.
	if samples := byName["ens18"]; len(samples) != trafficSamples || samples[0].RxBytes != 10 || samples[119].RxBytes != 129 || !samples[0].At.Equal(at(10)) {
		t.Fatalf("ens18 holds %d samples from %v", len(samples), samples[0])
	}
	if samples := byName["ens19"]; len(samples) != 5 || samples[0].RxBytes != 125 {
		t.Fatalf("a new interface holds %d samples", len(samples))
	}
	// An interface that has gone keeps what is not yet an hour old: ticks 10 to 19 at tick 129.
	if samples := byName["ens20"]; len(samples) != 10 || samples[0].RxBytes != 10 {
		t.Fatalf("a gone interface holds %d samples from %+v", len(samples), samples[0])
	}
	// And then nothing: it leaves the history when its last reading ages out.
	for tick := 130; tick < 141; tick++ {
		history.record(at(tick), map[string]host.Counters{"ens18": bytes(uint64(tick))})
	}
	if view = history.view(); len(view.Interfaces) != 2 {
		t.Fatalf("an interface gone for an hour is still there: %+v", view.Interfaces)
	}
	// What is handed out is a copy.
	view.Interfaces[0].Samples[0].RxBytes = 0
	if history.view().Interfaces[0].Samples[0].RxBytes == 0 {
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
	writeUnder(t, root, "/sys/class/net/ens18/statistics/rx_bytes", "1000\n")
	traffic.record(time.Now(), host.ReadCounters(root))
	rec = get("/api/v1/status/traffic")
	validResponse(t, "TrafficResponse", rec.Body.Bytes())
	if !strings.Contains(rec.Body.String(), `"name":"ens18"`) || !strings.Contains(rec.Body.String(), `"rxBytes":1000`) {
		t.Fatalf("%s", rec.Body)
	}
}
