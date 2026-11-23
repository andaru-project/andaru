// SPDX-License-Identifier: AGPL-3.0-or-later
package events

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// Type identifies a netplane state transition event matching schema/event.schema.json.
type Type string

const (
	DeviceOnline    Type = "device.online"
	DeviceOffline   Type = "device.offline"
	LinkUp          Type = "link.up"
	LinkDown        Type = "link.down"
	LinkStpChanged  Type = "link.stp_changed"
	SfpAlert        Type = "sfp.alert"
	VpnUp           Type = "vpn.up"
	VpnDown         Type = "vpn.down"
	GatewayUp       Type = "gateway.up"
	GatewayDown     Type = "gateway.down"
	ClientJoined    Type = "client.joined"
	ClientLeft      Type = "client.left"
	TopologyUpdated Type = "topology.updated"
)

// Severity classifies event urgency.
type Severity string

const (
	SeverityInfo Severity = "info"
	SeverityWarn Severity = "warn"
	SeverityCrit Severity = "crit"
)

// Event is one emitted netplane notification.
type Event struct {
	ID        string         `json:"id"`
	Type      Type           `json:"type"`
	Severity  Severity       `json:"severity"`
	Timestamp time.Time      `json:"timestamp"`
	TargetID  string         `json:"target_id,omitempty"`
	DeviceID  string         `json:"device_id,omitempty"`
	LinkName  string         `json:"link_name,omitempty"`
	Summary   string         `json:"summary"`
	Details   map[string]any `json:"details,omitempty"`
}

// Bus is a non-blocking, thread-safe in-process pub/sub event fan-out.
type Bus struct {
	mu      sync.RWMutex
	subs    map[uint64]chan Event
	nextID  atomic.Uint64
	dropped atomic.Uint64
}

// NewBus creates a new EventBus.
func NewBus() *Bus {
	return &Bus{
		subs: make(map[uint64]chan Event),
	}
}

// Subscribe registers a buffered event channel and returns an unsubscribe function.
func (b *Bus) Subscribe(bufferSize int) (<-chan Event, func()) {
	if bufferSize <= 0 {
		bufferSize = 64
	}
	id := b.nextID.Add(1)
	ch := make(chan Event, bufferSize)
	b.mu.Lock()
	b.subs[id] = ch
	b.mu.Unlock()

	var once sync.Once
	unsub := func() {
		once.Do(func() {
			b.mu.Lock()
			delete(b.subs, id)
			close(ch)
			b.mu.Unlock()
		})
	}
	return ch, unsub
}

// Publish fans out ev to all subscribers without blocking. If a subscriber's buffer is full,
// the event is dropped for that subscriber and DroppedCount is incremented.
func (b *Bus) Publish(ev Event) {
	if ev.Timestamp.IsZero() {
		ev.Timestamp = time.Now().UTC()
	}
	if ev.ID == "" {
		ev.ID = fmt.Sprintf("evt-%d-%d", ev.Timestamp.UnixNano(), b.nextID.Add(1))
	}

	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, ch := range b.subs {
		select {
		case ch <- ev:
		default:
			b.dropped.Add(1)
		}
	}
}

// DroppedCount returns the total number of events dropped due to full subscriber buffers.
func (b *Bus) DroppedCount() uint64 {
	return b.dropped.Load()
}
