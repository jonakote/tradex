package replay

import (
	"path/filepath"
	"testing"
	"time"

	"tradex/internal/domain"
	"tradex/internal/marketdata"
)

func TestCompileQuotesMergesBrokerQuotesBeforeSameTimeStrategyEvent(t *testing.T) {
	base, err := Load(filepath.Join("testdata", "eurusd_continuation_win.json"))
	if err != nil {
		t.Fatal(err)
	}
	strategyTime := time.Time{}
	for _, event := range base.Events {
		if event.Kind != EventQuote {
			strategyTime = event.At
			break
		}
	}
	dataset := marketdata.QuoteDataset{
		SchemaVersion: marketdata.QuoteDatasetSchema,
		Provenance: marketdata.QuoteProvenance{
			DatasetID: "quotes-for-replay", Vendor: "fixture", Feed: "paper-feed", Broker: "Pepperstone",
			RetrievedAt: time.Date(2026, 9, 1, 11, 0, 0, 0, time.UTC), SourceTimezone: "UTC",
			TimestampBasis: "UTC_EVENT_TIME", RawArtifactSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			LicenseReference: "fixture-license",
		},
		Streams: []marketdata.QuoteStream{{Symbol: "EURUSD", Quotes: []domain.Quote{
			{Source: "paper-feed", Symbol: "EURUSD", Bid: 108006, Ask: 108008, At: strategyTime, Sequence: 1},
		}}},
	}
	compiled, err := CompileQuotes(base, dataset)
	if err != nil {
		t.Fatal(err)
	}
	baseNonQuotes := 0
	for _, event := range base.Events {
		if event.Kind != EventQuote {
			baseNonQuotes++
		}
	}
	if compiled.QuoteDataset == nil || len(compiled.Events) != baseNonQuotes+1 {
		t.Fatalf("quote dataset/events missing: %+v", compiled)
	}
	for index, event := range compiled.Events {
		if event.At.Equal(strategyTime) && event.Kind == EventQuote {
			if index+1 >= len(compiled.Events) || compiled.Events[index+1].Kind == EventQuote {
				t.Fatal("quote was not placed before same-time strategy event")
			}
			return
		}
	}
	t.Fatal("compiled quote event not found")
}
