// SPDX-License-Identifier: AGPL-3.0-or-later
package driver

import (
	"fmt"
	"sort"
	"sync"
)

// Registry maps driver kinds ("openwrt", "pfsense", "unifi", "snmp", ...) to Driver implementations.
type Registry struct {
	mu      sync.RWMutex
	drivers map[string]Driver
}

// NewRegistry returns an empty thread-safe driver Registry.
func NewRegistry() *Registry {
	return &Registry{
		drivers: make(map[string]Driver),
	}
}

// Register adds d to the registry, rejecting empty or duplicate kinds.
func (r *Registry) Register(d Driver) error {
	if d == nil {
		return fmt.Errorf("driver is nil")
	}
	kind := d.Kind()
	if kind == "" {
		return fmt.Errorf("driver kind is empty")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.drivers[kind]; exists {
		return fmt.Errorf("driver %q already registered", kind)
	}
	r.drivers[kind] = d
	return nil
}

// Get returns the driver for kind.
func (r *Registry) Get(kind string) (Driver, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	d, ok := r.drivers[kind]
	return d, ok
}

// Kinds returns all registered driver kinds sorted alphabetically.
func (r *Registry) Kinds() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.drivers))
	for k := range r.drivers {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
