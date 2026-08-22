package ctrader

import (
	"strings"
	"testing"
	"time"

	"tradex/internal/config"
)

func readinessConfig() Config {
	return Config{Name: "pepperstone", AccountID: "acct", Environment: "paper", Mode: config.BrokerActiveDemo, Endpoint: "demo", ClientID: "client", CredentialReference: "ref", ManifestSHA256: strings.Repeat("a", 64)}
}

func TestCheckDeploymentReadinessFailsClosed(t *testing.T) {
	b, err := NewCircuitBreaker(1, time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	gate, err := NewSafetyGate(b, &KillSwitch{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	report := CheckDeploymentReadiness(ReadinessInput{Config: readinessConfig(), Reconciliation: Reconciliation{Match: false}, Evidence: OperationalEvidence{Healthy: false}, Safety: gate})
	if report.Ready || len(report.Reasons) < 2 {
		t.Fatalf("report = %#v", report)
	}
}

func TestCheckDeploymentReadinessPassesWithAllGates(t *testing.T) {
	b, _ := NewCircuitBreaker(1, time.Minute, nil)
	gate, _ := NewSafetyGate(b, &KillSwitch{}, nil)
	report := CheckDeploymentReadiness(ReadinessInput{Config: readinessConfig(), Reconciliation: Reconciliation{Match: true}, Evidence: OperationalEvidence{Healthy: true}, Safety: gate})
	if !report.Ready || len(report.Reasons) != 0 {
		t.Fatalf("report = %#v", report)
	}
}
