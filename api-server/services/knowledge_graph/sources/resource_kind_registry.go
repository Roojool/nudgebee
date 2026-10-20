package sources

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Per-resource identity registry.
//
// A cloud's inventory rows carry a type and a service name; which node a row
// becomes (node type, concrete label, subtype, which metadata extractor runs)
// is a fact about that resource, so it is declared in the resource's own file
// through the cloud's registerKind, next to the schema and the patch spec.
// This file holds only the lookup mechanics, shared by every cloud the way
// PatchSpecRegistry is.

// KindKey names the inventory rows one kind builds. Types nil registers the
// kind as the fallback for every type of Service that no exact kind claims —
// the role the per-cloud service fallback maps used to play. Type keys match
// lower-cased; Service matches as the collector writes it.
type KindKey struct {
	Service string
	Types   []string
}

// KindKeyed is a cloud's kind payload: anything that can name the rows it builds.
type KindKeyed interface {
	KindKey() KindKey
}

type kindExactKey struct {
	rowType string
	service string
}

// ResourceKindRegistry resolves an inventory row's (type, service) to the
// cloud's kind payload. Resolution order is the caller's: LookupType for the
// exact key, then LookupService for the service fallback.
type ResourceKindRegistry[K KindKeyed] struct {
	mu       sync.RWMutex
	exact    map[kindExactKey]K
	fallback map[string]K
	ordered  []K
}

// NewResourceKindRegistry returns an empty registry.
func NewResourceKindRegistry[K KindKeyed]() *ResourceKindRegistry[K] {
	return &ResourceKindRegistry[K]{
		exact:    make(map[kindExactKey]K),
		fallback: make(map[string]K),
	}
}

// Register adds a kind. It panics on a service fallback with no service, on an
// empty type and on a key registered twice: all are programming errors in a
// resource file, and an init-time panic surfaces them in every test run.
func (r *ResourceKindRegistry[K]) Register(kind K) {
	key := kind.KindKey()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(key.Types) == 0 {
		if key.Service == "" {
			panic("resource kind declares neither a type nor a service")
		}
		if _, exists := r.fallback[key.Service]; exists {
			panic(fmt.Sprintf("resource kind fallback for service %q is already registered", key.Service))
		}
		r.fallback[key.Service] = kind
		r.ordered = append(r.ordered, kind)
		return
	}
	for _, rowType := range key.Types {
		if rowType == "" {
			panic(fmt.Sprintf("resource kind for service %q declares an empty type", key.Service))
		}
		exact := kindExactKey{rowType: strings.ToLower(rowType), service: key.Service}
		if _, exists := r.exact[exact]; exists {
			panic(fmt.Sprintf("resource kind for type %q of service %q is already registered", rowType, key.Service))
		}
		r.exact[exact] = kind
	}
	r.ordered = append(r.ordered, kind)
}

// LookupType returns the kind registered for this exact (type, service).
func (r *ResourceKindRegistry[K]) LookupType(rowType, service string) (K, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	kind, ok := r.exact[kindExactKey{rowType: strings.ToLower(rowType), service: service}]
	return kind, ok
}

// LookupService returns the kind registered as service's fallback.
func (r *ResourceKindRegistry[K]) LookupService(service string) (K, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	kind, ok := r.fallback[service]
	return kind, ok
}

// Kinds returns every registered kind, sorted by service then first type
// (fallbacks, which have no type, first within a service), so the coverage
// tests that enumerate a cloud's resources read a stable table.
func (r *ResourceKindRegistry[K]) Kinds() []K {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]K, len(r.ordered))
	copy(out, r.ordered)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i].KindKey(), out[j].KindKey()
		if a.Service != b.Service {
			return a.Service < b.Service
		}
		return firstType(a) < firstType(b)
	})
	return out
}

func firstType(key KindKey) string {
	if len(key.Types) == 0 {
		return ""
	}
	return strings.ToLower(key.Types[0])
}
