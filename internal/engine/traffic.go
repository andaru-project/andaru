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
	// Often enough to show a dip of a few seconds. The history is in memory only, so the cost is small: a
	// reading is under 100 bytes, an hour of one interface about 64 KiB, and a hundred interfaces a few megabytes.
	trafficInterval = 5 * time.Second
	// An hour of readings per interface.
	trafficSamples = 720
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
	IntervalSeconds int `json:"intervalSeconds"`
	// Complete says the answer is the whole history, to replace what a client holds, not readings to add to it.
	Complete   bool               `json:"complete"`
	Interfaces []trafficInterface `json:"interfaces"`
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
		// To the millisecond, which is what a browser's clock keeps: a client can send a reading's time back as it is.
		samples := append(h.samples[name], trafficSample{At: now.UTC().Truncate(time.Millisecond), Counters: reading})
		// The age rule above keeps an hour at the usual pace; this bounds a history read faster than that.
		h.samples[name] = samples[max(len(samples)-trafficSamples, 0):]
	}
}

// view copies out the readings taken after since, interfaces by name. Every interface the history holds is
// named, also with no reading that new, so a client that asks only for what it lacks still learns which
// interfaces there are. The zero time gives the whole hour, and so does a since the history does not reach: a
// client ahead of every reading held is from before the clock was set back or the engine restarted, and
// would otherwise be sent nothing until the clock caught up.
// shortcut: the whole hour is about 120 KiB of JSON an interface; send columns, or one interface at a time,
// when boxes with dozens of interfaces matter.
func (h *trafficHistory) view(since time.Time) trafficResponse {
	h.mu.Lock()
	defer h.mu.Unlock()
	response := trafficResponse{IntervalSeconds: int(trafficInterval / time.Second), Interfaces: make([]trafficInterface, 0, len(h.samples))}
	newest := time.Time{}
	for _, samples := range h.samples {
		if at := samples[len(samples)-1].At; at.After(newest) {
			newest = at
		}
	}
	if since.After(newest) {
		since = time.Time{}
	}
	response.Complete = since.IsZero()
	for name, samples := range h.samples {
		// Readings run oldest first, so what is new is a tail.
		first, _ := slices.BinarySearchFunc(samples, since, func(sample trafficSample, since time.Time) int {
			if sample.At.After(since) {
				return 1
			}
			return -1
		})
		response.Interfaces = append(response.Interfaces, trafficInterface{Name: name, Samples: slices.Clone(samples[first:])})
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
