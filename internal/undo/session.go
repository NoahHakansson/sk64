package undo

import (
	"bytes"
	"slices"
)

// Capacity is the maximum number of saves remembered in memory.
const Capacity = 20

// Entry records the state a save overwrote. Exactly one save is stored per
// entry. Multi-key entries contain valid UTF-8 values from whole-resource
// edits; single-key entries may contain binary bytes.
type Entry struct {
	Context   string
	Kind      string
	Namespace string
	Name      string
	Previous  map[string][]byte
	Added     []string
}

// Ring is a single-goroutine bounded history of recent saves.
type Ring struct {
	entries []Entry
}

// NewRing creates an empty session history.
func NewRing() *Ring { return &Ring{} }

// Push appends an entry and evicts the oldest entry beyond Capacity.
func (r *Ring) Push(entry Entry) {
	entry = cloneEntry(entry)
	if len(r.entries) == Capacity {
		copy(r.entries, r.entries[1:])
		r.entries[Capacity-1] = entry
		return
	}
	r.entries = append(r.entries, entry)
}

// Len returns the number of remembered saves.
func (r *Ring) Len() int { return len(r.entries) }

// LatestFor returns the newest matching resource entry without consuming it.
func (r *Ring) LatestFor(kubeContext, kind, namespace, name string) (Entry, bool) {
	for i := len(r.entries) - 1; i >= 0; i-- {
		entry := r.entries[i]
		if entry.Context == kubeContext && entry.Kind == kind && entry.Namespace == namespace && entry.Name == name {
			return cloneEntry(entry), true
		}
	}
	return Entry{}, false
}

func cloneEntry(entry Entry) Entry {
	if entry.Previous != nil {
		previous := make(map[string][]byte, len(entry.Previous))
		for key, value := range entry.Previous {
			previous[key] = bytes.Clone(value)
		}
		entry.Previous = previous
	}
	entry.Added = slices.Clone(entry.Added)
	return entry
}
