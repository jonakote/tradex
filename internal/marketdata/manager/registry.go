package manager

import (
	"errors"
	"fmt"
	"sync"
)

// ProviderPriority defines the failover chain for a symbol.
type ProviderPriority struct {
	Symbol    string   `json:"symbol"`
	Providers []string `json:"providers"` // Ordered: primary, secondary, tertiary...
}

// RegistryConfig defines provider routing rules.
type RegistryConfig struct {
	// DefaultProviders is the fallback priority when no symbol-specific rule exists.
	DefaultProviders []string `json:"default_providers"`
	// SymbolRouting maps specific symbols to their provider priority chain.
	SymbolRouting []ProviderPriority `json:"symbol_routing"`
}

func (c RegistryConfig) Validate() error {
	if len(c.DefaultProviders) == 0 {
		return errors.New("registry requires at least one default provider")
	}
	return nil
}

// Registry manages registered providers and routing rules.
type Registry struct {
	mu        sync.RWMutex
	providers map[string]MarketDataProvider
	routing   map[string][]string // symbol -> ordered provider names
	defaults  []string
}

// NewRegistry creates a provider registry with the given configuration.
func NewRegistry(cfg RegistryConfig) (*Registry, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	routing := make(map[string][]string)
	for _, rule := range cfg.SymbolRouting {
		if rule.Symbol == "" || len(rule.Providers) == 0 {
			continue
		}
		routing[rule.Symbol] = rule.Providers
	}
	return &Registry{
		providers: make(map[string]MarketDataProvider),
		routing:   routing,
		defaults:  cfg.DefaultProviders,
	}, nil
}

// Register adds a provider to the registry.
func (r *Registry) Register(provider MarketDataProvider) error {
	if provider == nil {
		return errors.New("cannot register nil provider")
	}
	name := provider.Name()
	if name == "" {
		return errors.New("provider name is empty")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.providers[name]; exists {
		return fmt.Errorf("provider %q already registered", name)
	}
	r.providers[name] = provider
	return nil
}

// Get returns a provider by name.
func (r *Registry) Get(name string) (MarketDataProvider, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.providers[name]
	return p, ok
}

// ProvidersFor returns the ordered provider chain for a symbol.
// It checks symbol-specific routing first, then falls back to defaults.
func (r *Registry) ProvidersFor(symbol string) []MarketDataProvider {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := r.routing[symbol]
	if len(names) == 0 {
		names = r.defaults
	}

	result := make([]MarketDataProvider, 0, len(names))
	for _, name := range names {
		if p, ok := r.providers[name]; ok {
			result = append(result, p)
		}
	}
	return result
}

// All returns all registered providers.
func (r *Registry) All() []MarketDataProvider {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]MarketDataProvider, 0, len(r.providers))
	for _, p := range r.providers {
		result = append(result, p)
	}
	return result
}

// Names returns the names of all registered providers.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.providers))
	for name := range r.providers {
		names = append(names, name)
	}
	return names
}
