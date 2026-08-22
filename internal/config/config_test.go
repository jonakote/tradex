package config

import "testing"

func TestRuntimeAllowsOnlyOneActiveBroker(t *testing.T) {
	cfg := Runtime{
		Environment: "paper", Risk: DefaultRisk(),
		Brokers: []Broker{
			{Name: "pepperstone", Adapter: "ctrader", Mode: BrokerActiveDemo, AccountID: "p1", BaseCurrency: "USD"},
			{Name: "exness", Adapter: "mt5", Mode: BrokerActiveDemo, AccountID: "e1", BaseCurrency: "USD"},
		},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("multiple active brokers accepted")
	}

	cfg.Brokers[1].Mode = BrokerShadow
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid active/shadow setup rejected: %v", err)
	}
}
