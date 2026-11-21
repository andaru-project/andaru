package engine

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/andaru-project/andaru/internal/host"
)

const (
	trafficInterval = 30 * time.Second
	// An hour of readings per interface.
	trafficSamples = 120
)

// trafficSample is one interface's counters at one moment.
type trafficSample struct {
	At time.Time `json:"at"`
	host.Counters
}

type trafficInterface struct {
	Name    string          `json:"name"`
	Samples []trafficSample `json:"samples"`
}

type trafficResponse struct {
	IntervalSeconds int                `json:"intervalSeconds"`
	Interfaces      []trafficInterface `json:"interfaces"`
}

// trafficHistory keeps the last hour of every network interface's counters, so the dashboard's graph is whole
// when it opens. It lives in memory only: it starts again with the engine, and nothing is written to disk.
type trafficHistory struct {
	mu      sync.Mutex
	samples map[string][]trafficSample
}

// traffic is the engine's one history. The sampler fills it; the status route reads it.
var traffic = &trafficHistory{samples: map[string][]trafficSample{}}

// record adds one reading of every interface and drops what is older than the hour. An interface that has
// gone keeps its readings until they age out, so its last hour stays on the graph. A reading stamped later than
// now is from before the clock was set back: it goes too, or samples would no longer run oldest first.
func (h *trafficHistory) record(now time.Time, counters map[string]host.Counters) {
	h.mu.Lock()
	defer h.mu.Unlock()
	oldest := now.Add(-trafficSamples * trafficInterval)
	for name, samples := range h.samples {
		samples = slices.DeleteFunc(samples, func(sample trafficSample) bool { return !sample.At.After(oldest) || !sample.At.Before(now) })
		if len(samples) == 0 {
			delete(h.samples, name)
		} else {
			h.samples[name] = samples
		}
	}
	for name, reading := range counters {
		samples := append(h.samples[name], trafficSample{At: now.UTC(), Counters: reading})
		// The age rule above keeps an hour at the usual pace; this bounds a history read faster than that.
		h.samples[name] = samples[max(len(samples)-trafficSamples, 0):]
	}
}

// view copies the history out, interfaces by name.
func (h *trafficHistory) view() trafficResponse {
	h.mu.Lock()
	defer h.mu.Unlock()
	response := trafficResponse{IntervalSeconds: int(trafficInterval / time.Second), Interfaces: make([]trafficInterface, 0, len(h.samples))}
	for name, samples := range h.samples {
		response.Interfaces = append(response.Interfaces, trafficInterface{Name: name, Samples: slices.Clone(samples)})
	}
	slices.SortFunc(response.Interfaces, func(a, b trafficInterface) int { return strings.Compare(a.Name, b.Name) })
	return response
}

// sampleTraffic reads the counters under root now and every interval until the context ends.
func sampleTraffic(ctx context.Context, history *trafficHistory, root string) {
	ticker := time.NewTicker(trafficInterval)
	defer ticker.Stop()
	for {
		history.record(time.Now(), host.ReadCounters(root))
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
