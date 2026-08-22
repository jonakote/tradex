package marketdata

import (
	"strings"
	"testing"
	"time"
)

func TestNormalizeQuotesResolvesAliasAndUnixMillis(t *testing.T) {
	raw := RawQuoteDataset{SchemaVersion: RawQuoteDatasetSchema, Provenance: QuoteProvenance{DatasetID: "raw", Vendor: "fixture", Feed: "feed", Broker: "Pepperstone", RetrievedAt: time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC), SourceTimezone: "UTC", RawArtifactSHA256: strings.Repeat("a", 64), LicenseReference: "fixture"}, Streams: []RawQuoteStream{{Symbol: "EUR/USD", Quotes: []RawQuote{{Source: "feed", Symbol: "EUR/USD", Bid: 108400, Ask: 108402, Timestamp: "1785568800000", Sequence: 1}}}}}
	normalized, err := NormalizeQuotes(raw, QuoteNormalization{TimestampBasis: "UNIX_MILLIS", SymbolAliases: map[string]string{"EUR/USD": "EURUSD"}})
	if err != nil {
		t.Fatal(err)
	}
	if normalized.Streams[0].Symbol != "EURUSD" || normalized.Streams[0].Quotes[0].At.Location() != time.UTC {
		t.Fatalf("unexpected normalized quote: %+v", normalized)
	}
}

func TestNormalizeQuotesRejectsAliasCollision(t *testing.T) {
	raw := RawQuoteDataset{SchemaVersion: RawQuoteDatasetSchema, Provenance: QuoteProvenance{DatasetID: "raw", Vendor: "fixture", Feed: "feed", Broker: "Pepperstone", RetrievedAt: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), SourceTimezone: "UTC", RawArtifactSHA256: strings.Repeat("a", 64), LicenseReference: "fixture"}, Streams: []RawQuoteStream{{Symbol: "A", Quotes: []RawQuote{{Source: "feed", Symbol: "A", Bid: 1, Ask: 2, Timestamp: "2026-08-01T00:00:00Z", Sequence: 1}}}, {Symbol: "B", Quotes: []RawQuote{{Source: "feed", Symbol: "B", Bid: 1, Ask: 2, Timestamp: "2026-08-01T00:00:01Z", Sequence: 1}}}}}
	if _, err := NormalizeQuotes(raw, QuoteNormalization{TimestampBasis: "RFC3339", SymbolAliases: map[string]string{"A": "EURUSD", "B": "EURUSD"}}); err == nil {
		t.Fatal("accepted alias collision")
	}
}
