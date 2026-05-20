package provider

import (
	"sort"
	"sync"
)

// Registry is a thread-safe map of providers keyed by Name(). Adapters
// register themselves at startup; the relay and HTTP layer look them up
// by name when routing webhooks and outbound messages.
type Registry struct {
	mu        sync.RWMutex
	providers map[string]Provider
}

func NewRegistry() *Registry {
	return &Registry{providers: make(map[string]Provider)}
}

// Register installs p under its Name(). Returns ErrDuplicateProvider if
// another provider with that name is already registered. Name() is
// trusted verbatim — adapters are responsible for stable, lowercase ids.
func (r *Registry) Register(p Provider) error {
	if p == nil {
		return ErrUnknownProvider
	}
	name := p.Name()
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.providers[name]; exists {
		return ErrDuplicateProvider
	}
	r.providers[name] = p
	return nil
}

func (r *Registry) Get(name string) (Provider, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.providers[name]
	return p, ok
}

// Names returns the sorted list of registered provider names. Callers use
// this for diagnostics and admin-UI listing; the sort is for stable output.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.providers))
	for n := range r.providers {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
