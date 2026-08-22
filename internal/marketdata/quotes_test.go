package marketdata

import (
	"strings"
	"testing"
	"time"

	"tradex/internal/domain"
)

func testQuoteDataset() QuoteDataset {
	return QuoteDataset{
		SchemaVersion: QuoteDatasetSchema,
		Provenance: QuoteProvenance{
			DatasetID: "pepperstone-eurusd-ticks", Vendor: "fixture", Feed: "pepperstone-demo",
			Broker: "Pepperstone", RetrievedAt: time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC),
			SourceTimezone: "UTC", TimestampBasis: "UTC_EVENT_TIME",
			RawArtifactSHA256: strings.Repeat("a", 64), LicenseReference: "fixture-license",
		},
		MaxGap: time.Minute,
		Streams: []QuoteStream{{Symbol: "EURUSD", Quotes: []domain.Quote{
			{Source: "pepperstone-demo", Symbol: "EURUSD", Bid: 108400, Ask: 108402, At: time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC), Sequence: 1},
			{Source: "pepperstone-demo", Symbol: "EURUSD", Bid: 108401, Ask: 108403, At: time.Date(2026, 8, 18, 10, 0, 1, 0, time.UTC), Sequence: 2},
		}}},
	}
}

func TestQuoteDatasetValidatesAndFingerprints(t *testing.T) {
	dataset := testQuoteDataset()
	first, err := dataset.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	dataset.Streams[0].Quotes[1].Ask++
	second, err := dataset.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("quote fingerprint did not change after quote mutation")
	}
}

func TestQuoteDatasetRejectsSequenceAndUndeclaredGap(t *testing.T) {
	dataset := testQuoteDataset()
	dataset.Streams[0].Quotes[1].Sequence = 1
	if err := dataset.Validate(); err == nil {
		t.Fatal("accepted duplicate quote sequence")
	}
	dataset = testQuoteDataset()
	dataset.Streams[0].Quotes[1].At = dataset.Streams[0].Quotes[0].At.Add(2 * time.Minute)
	if err := dataset.Validate(); err == nil {
		t.Fatal("accepted undeclared quote gap")
	}
	dataset.AllowGaps = true
	if err := dataset.Validate(); err != nil {
		t.Fatal(err)
	}
}
