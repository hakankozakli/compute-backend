package registry

import "sync"

// Entry maps an external fal.ai model identifier to an internal service target.
type Entry struct {
    ExternalID string
    TargetURL  string
    SupportsWS bool
}

// Registry offers a threadsafe in-memory implementation for bootstrapping.
type Registry struct {
    mu      sync.RWMutex
    entries map[string]Entry
}

// New returns an empty registry ready for population from configuration or DB.
func New() *Registry {
    return &Registry{entries: map[string]Entry{}}
}

// Set stores or updates a registry entry.
func (r *Registry) Set(entry Entry) {
    r.mu.Lock()
    defer r.mu.Unlock()
    r.entries[entry.ExternalID] = entry
}

// Get retrieves a registry entry by external ID and a boolean indicating its presence.
func (r *Registry) Get(externalID string) (Entry, bool) {
    r.mu.RLock()
    defer r.mu.RUnlock()
    entry, ok := r.entries[externalID]
    return entry, ok
}
