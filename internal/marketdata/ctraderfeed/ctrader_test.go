package ctraderfeed

import (
	"context"
	"testing"
	"time"

	"tradex/internal/marketdata/manager"
	"tradex/internal/marketdata/manager/providers"
)

type fakeCTraderFeedClient struct {
	bars []providers.CTraderTrendbar
}

func (f *fakeCTraderFeedClient) GetSpot(context.Context, string, int64) (providers.CTraderSpot, error) {
	return providers.CTraderSpot{}, nil
}

func (f *fakeCTraderFeedClient) GetTrendbars(context.Context, string, int64, providers.CTraderPeriod, time.Time, time.Time, int) ([]providers.CTraderTrendbar, error) {
	return append([]providers.CTraderTrendbar(nil), f.bars...), nil
}

func (f *fakeCTraderFeedClient) SubscribeSpots(context.Context, string, []int64, func(providers.CTraderSpot) error) error {
	return nil
}

func (f *fakeCTraderFeedClient) SubscribeTrendbars(context.Context, string, int64, providers.CTraderPeriod, func(providers.CTraderTrendbar) error) error {
	return nil
}

func (f *fakeCTraderFeedClient) Close() error { return nil }

func TestFeedFetchesAndDeduplicatesCandles(t *testing.T) {
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	client := &fakeCTraderFeedClient{bars: []providers.CTraderTrendbar{
		{SymbolID: 1, Period: providers.CTraderPeriodH1, OpenTime: now.Add(-time.Hour), Low: 114600, DeltaOpen: 20, DeltaHigh: 90, DeltaClose: 50, Volume: 100},
		{SymbolID: 1, Period: providers.CTraderPeriodH1, OpenTime: now, Low: 114700, DeltaOpen: 10, DeltaHigh: 80, DeltaClose: 40, Volume: 120},
	}}
	provider, err := providers.NewCTraderProvider(client, providers.CTraderConfig{
		AccountID: "123", Environment: "demo", Symbols: []providers.CTraderSymbol{{
			SymbolID: 1, Symbol: "EUR/USD", Type: manager.InstrumentForex, Digits: 5,
		}},
	}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	f := &Feed{
		provider:  provider,
		transport: client,
		config:    Config{AccountID: "123", Environment: "demo", Interval: "1h", Symbols: []Symbol{{SymbolID: 1, Symbol: "EUR/USD", Type: manager.InstrumentForex, Digits: 5}}},
		now:       func() time.Time { return now },
		last:      make(map[string]time.Time),
	}

	first, err := f.FetchAllSymbols(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 2 || first[0].Symbol != "EUR/USD" || first[0].Interval != "1h" || first[1].Close != 1.1474 {
		t.Fatalf("unexpected first cTrader fetch: %+v", first)
	}
	second, err := f.FetchAllSymbols(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 0 {
		t.Fatalf("expected duplicate candles to be filtered, got %+v", second)
	}
}

func TestConfigRequiresExplicitIdentity(t *testing.T) {
	config := Config{Environment: "demo", AccountID: "123", TokenFile: "token.json", Interval: "1h"}
	if err := config.Validate(); err == nil {
		t.Fatal("expected missing symbols to be rejected")
	}
	config.Symbols = []Symbol{{SymbolID: 1, Symbol: "EUR/USD", Type: manager.InstrumentForex, Digits: 5}}
	if err := config.Validate(); err != nil {
		t.Fatal(err)
	}
}
