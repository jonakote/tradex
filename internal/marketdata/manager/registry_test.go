package manager

import (
	"testing"
)

func TestRegistryRegisterAndGet(t *testing.T) {
	reg, err := NewRegistry(RegistryConfig{DefaultProviders: []string{"alpha"}})
	if err != nil {
		t.Fatal(err)
	}

	provider := &fakeProvider{name: "alpha", supported: map[string]bool{"EUR/USD": true}}
	if err := reg.Register(provider); err != nil {
		t.Fatal(err)
	}

	got, ok := reg.Get("alpha")
	if !ok || got.Name() != "alpha" {
		t.Fatal("provider not found")
	}

	// Duplicate registration
	if err := reg.Register(provider); err == nil {
		t.Fatal("accepted duplicate registration")
	}
}

func TestRegistryRejectsNil(t *testing.T) {
	reg, _ := NewRegistry(RegistryConfig{DefaultProviders: []string{"x"}})
	if err := reg.Register(nil); err == nil {
		t.Fatal("accepted nil provider")
	}
}

func TestRegistryProvidersForSymbolRouting(t *testing.T) {
	reg, _ := NewRegistry(RegistryConfig{
		DefaultProviders: []string{"alpha", "beta"},
		SymbolRouting: []ProviderPriority{
			{Symbol: "XAG/USD", Providers: []string{"beta", "alpha"}},
		},
	})

	alpha := &fakeProvider{name: "alpha", supported: map[string]bool{"EUR/USD": true, "XAG/USD": true}}
	beta := &fakeProvider{name: "beta", supported: map[string]bool{"XAG/USD": true}}
	reg.Register(alpha)
	reg.Register(beta)

	// EUR/USD should use defaults: alpha, beta
	providers := reg.ProvidersFor("EUR/USD")
	if len(providers) != 2 || providers[0].Name() != "alpha" {
		t.Fatalf("EUR/USD: expected [alpha, beta], got %v", providerNames(providers))
	}

	// XAG/USD should use symbol-specific: beta, alpha
	providers = reg.ProvidersFor("XAG/USD")
	if len(providers) != 2 || providers[0].Name() != "beta" {
		t.Fatalf("XAG/USD: expected [beta, alpha], got %v", providerNames(providers))
	}
}

func TestRegistryProvidersForUnregistered(t *testing.T) {
	reg, _ := NewRegistry(RegistryConfig{
		DefaultProviders: []string{"alpha", "missing"},
	})
	alpha := &fakeProvider{name: "alpha"}
	reg.Register(alpha)

	// "missing" isn't registered, should be skipped
	providers := reg.ProvidersFor("EUR/USD")
	if len(providers) != 1 || providers[0].Name() != "alpha" {
		t.Fatalf("expected [alpha], got %v", providerNames(providers))
	}
}

func TestRegistryAll(t *testing.T) {
	reg, _ := NewRegistry(RegistryConfig{DefaultProviders: []string{"a"}})
	reg.Register(&fakeProvider{name: "a"})
	reg.Register(&fakeProvider{name: "b"})

	all := reg.All()
	if len(all) != 2 {
		t.Fatalf("expected 2 providers, got %d", len(all))
	}
}

func TestRegistryConfigValidation(t *testing.T) {
	if _, err := NewRegistry(RegistryConfig{}); err == nil {
		t.Fatal("accepted empty default providers")
	}
}

func providerNames(providers []MarketDataProvider) []string {
	names := make([]string, len(providers))
	for i, p := range providers {
		names[i] = p.Name()
	}
	return names
}
