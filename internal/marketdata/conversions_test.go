package marketdata

import (
	"strings"
	"testing"
	"time"
)

func TestConversionDatasetRateAtUsesLatestFreshObservation(t *testing.T) {
	dataset := ConversionDataset{SchemaVersion: ConversionDatasetSchema, AccountCurrency: "USD", Provenance: ConversionProvenance{DatasetID: "usd-jpy", Vendor: "fixture", Feed: "fx", Broker: "Pepperstone", RetrievedAt: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), RawArtifactSHA256: strings.Repeat("c", 64), LicenseReference: "fixture"}, Streams: []ConversionStream{{From: "JPY", To: "USD", Observations: []ConversionObservation{{From: "JPY", To: "USD", Numerator: 68, Denominator: 10000, Source: "fx", ObservedAt: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)}, {From: "JPY", To: "USD", Numerator: 69, Denominator: 10000, Source: "fx", ObservedAt: time.Date(2026, 8, 1, 0, 1, 0, 0, time.UTC)}}}}}
	rate, err := dataset.RateAt("JPY", "USD", time.Date(2026, 8, 1, 0, 1, 30, 0, time.UTC), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if rate.Numerator != 69 {
		t.Fatalf("unexpected rate: %+v", rate)
	}
	if _, err := dataset.RateAt("JPY", "USD", time.Date(2026, 8, 1, 0, 3, 0, 0, time.UTC), time.Minute); err == nil {
		t.Fatal("accepted stale conversion")
	}
}
