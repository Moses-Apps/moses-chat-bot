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

// Replace installs p under its Name(), overwriting any existing provider with
// that name. Unlike Register it never errors on a duplicate — it is the
// runtime-reconfiguration path used when a tenant admin (re-)connects a bot
// through the in-app wizard, which must swap the live adapter without a
// process restart.
func (r *Registry) Replace(p Provider) error {
	if p == nil {
		return ErrUnknownProvider
	}
	name := p.Name()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.providers[name] = p
	return nil
}

// Remove drops the provider registered under name, if any. Used by the
// Disconnect path so outbound sends fail cleanly ("no provider") once a tenant
// admin disconnects their bot.
func (r *Registry) Remove(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.providers, name)
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
