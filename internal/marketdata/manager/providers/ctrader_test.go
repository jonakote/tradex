package providers

import (
	"context"
	"math"
	"testing"
	"time"

	"tradex/internal/marketdata/manager"
)

type fakeCTraderDataClient struct {
	spot       CTraderSpot
	trendbars  []CTraderTrendbar
	spotEvents []CTraderSpot
	barEvents  []CTraderTrendbar
	accountID  string
	symbolID   int64
	period     CTraderPeriod
}

func (f *fakeCTraderDataClient) GetSpot(_ context.Context, accountID string, symbolID int64) (CTraderSpot, error) {
	f.accountID, f.symbolID = accountID, symbolID
	return f.spot, nil
}

func (f *fakeCTraderDataClient) GetTrendbars(_ context.Context, accountID string, symbolID int64, period CTraderPeriod, _ time.Time, _ time.Time, _ int) ([]CTraderTrendbar, error) {
	f.accountID, f.symbolID, f.period = accountID, symbolID, period
	return append([]CTraderTrendbar(nil), f.trendbars...), nil
}

func (f *fakeCTraderDataClient) SubscribeSpots(_ context.Context, accountID string, symbolIDs []int64, handler func(CTraderSpot) error) error {
	f.accountID = accountID
	for _, event := range f.spotEvents {
		if err := handler(event); err != nil {
			return err
		}
	}
	if len(symbolIDs) != 1 || symbolIDs[0] != 42 {
		panic("unexpected cTrader spot subscription")
	}
	return nil
}

func (f *fakeCTraderDataClient) SubscribeTrendbars(_ context.Context, accountID string, symbolID int64, period CTraderPeriod, handler func(CTraderTrendbar) error) error {
	f.accountID, f.symbolID, f.period = accountID, symbolID, period
	for _, event := range f.barEvents {
		if err := handler(event); err != nil {
			return err
		}
	}
	return nil
}

func testCTraderProvider(t *testing.T, client *fakeCTraderDataClient) *CTraderProvider {
	t.Helper()
	provider, err := NewCTraderProvider(client, CTraderConfig{
		AccountID:   "demo-account",
		Environment: "demo",
		Symbols: []CTraderSymbol{{
			SymbolID: 42,
			Symbol:   "EUR/USD",
			Type:     manager.InstrumentForex,
			Digits:   5,
		}},
	}, func() time.Time { return time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC) })
	if err != nil {
		t.Fatal(err)
	}
	return provider
}

func TestCTraderProviderConvertsSnapshotQuote(t *testing.T) {
	client := &fakeCTraderDataClient{spot: CTraderSpot{
		SymbolID:  42,
		Bid:       108400,
		Ask:       108402,
		Timestamp: time.Date(2026, 9, 21, 11, 59, 59, 0, time.UTC),
	}}
	provider := testCTraderProvider(t, client)

	quote, err := provider.GetQuote(context.Background(), manager.QuoteRequest{Symbol: "EUR/USD"})
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(quote.Bid-1.084) > 1e-9 || math.Abs(quote.Ask-1.08402) > 1e-9 || math.Abs(quote.Price-1.08401) > 1e-9 {
		t.Fatalf("unexpected normalized quote: %+v", quote)
	}
	if quote.Provider != "ctrader" || quote.FetchedAt.IsZero() {
		t.Fatalf("unexpected quote metadata: %+v", quote)
	}
	if client.accountID != "demo-account" || client.symbolID != 42 {
		t.Fatalf("unexpected request identity: account=%q symbol=%d", client.accountID, client.symbolID)
	}
}

func TestCTraderProviderConvertsAndOrdersTrendbars(t *testing.T) {
	client := &fakeCTraderDataClient{trendbars: []CTraderTrendbar{
		{
			SymbolID: 42, Period: CTraderPeriodH1,
			OpenTime: time.Date(2026, 9, 21, 11, 0, 0, 0, time.UTC),
			Low:      108400, DeltaOpen: 10, DeltaHigh: 120, DeltaClose: 50, Volume: 12,
		},
		{
			SymbolID: 42, Period: CTraderPeriodH1,
			OpenTime: time.Date(2026, 9, 21, 10, 0, 0, 0, time.UTC),
			Low:      108300, DeltaOpen: 20, DeltaHigh: 90, DeltaClose: 40, Volume: 9,
		},
	}}
	provider := testCTraderProvider(t, client)

	candles, err := provider.GetCandles(context.Background(), manager.CandleRequest{
		Symbol: "EUR/USD", Timeframe: manager.Timeframe1h, Limit: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(candles) != 2 || !candles[0].OpenTime.Before(candles[1].OpenTime) {
		t.Fatalf("candles were not sorted: %+v", candles)
	}
	if candles[1].Open != 1.08410 || candles[1].High != 1.08520 || candles[1].Close != 1.08450 {
		t.Fatalf("unexpected normalized candle: %+v", candles[1])
	}
	if client.period != CTraderPeriodH1 {
		t.Fatalf("unexpected cTrader period %q", client.period)
	}
}

func TestCTraderProviderStreamsQuotesAndBars(t *testing.T) {
	at := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	client := &fakeCTraderDataClient{
		spotEvents: []CTraderSpot{{SymbolID: 42, Bid: 108400, Ask: 108402, Timestamp: at}},
		barEvents: []CTraderTrendbar{{
			SymbolID: 42, Period: CTraderPeriodM15, OpenTime: at.Add(-15 * time.Minute),
			Low: 108400, DeltaOpen: 10, DeltaHigh: 120, DeltaClose: 50, Volume: 12,
		}},
	}
	provider := testCTraderProvider(t, client)

	var quotes []manager.Quote
	if err := provider.SubscribeQuotes(context.Background(), func(quote manager.Quote) error {
		quotes = append(quotes, quote)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(quotes) != 1 || quotes[0].Symbol != "EUR/USD" {
		t.Fatalf("unexpected streamed quotes: %+v", quotes)
	}

	var candles []manager.Candle
	if err := provider.SubscribeCandles(context.Background(), "EUR/USD", manager.Timeframe15m, func(candle manager.Candle) error {
		candles = append(candles, candle)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(candles) != 1 || candles[0].Timeframe != manager.Timeframe15m {
		t.Fatalf("unexpected streamed candles: %+v", candles)
	}
}

func TestCTraderProviderRejectsUnknownOrInvalidData(t *testing.T) {
	client := &fakeCTraderDataClient{spot: CTraderSpot{SymbolID: 99, Bid: 1, Ask: 1, Timestamp: time.Now()}}
	provider := testCTraderProvider(t, client)
	if _, err := provider.GetQuote(context.Background(), manager.QuoteRequest{Symbol: "GBP/USD"}); err == nil {
		t.Fatal("unknown symbol was accepted")
	}

	client.spot = CTraderSpot{SymbolID: 42, Bid: 108402, Ask: 108400, Timestamp: time.Now()}
	if _, err := provider.GetQuote(context.Background(), manager.QuoteRequest{Symbol: "EUR/USD"}); err == nil {
		t.Fatal("invalid bid/ask was accepted")
	}
}
