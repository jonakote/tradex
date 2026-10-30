package providers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"tradex/internal/feed"
	"tradex/internal/marketdata/manager"
)

func TestBinanceProviderSupportsCryptoOnly(t *testing.T) {
	client, err := feed.NewBinanceClient(feed.BinanceConfig{Symbols: []string{"BTCUSDT"}, Interval: "1h"})
	if err != nil {
		t.Fatal(err)
	}
	p := NewBinanceProvider(client)
	if p.Name() != "binance" {
		t.Fatalf("expected binance provider, got %q", p.Name())
	}
	if !p.Supports(manager.Instrument{Symbol: "BTCUSDT", Type: manager.InstrumentCrypto}) {
		t.Fatal("expected Binance to support crypto")
	}
	if p.Supports(manager.Instrument{Symbol: "EUR/USD", Type: manager.InstrumentForex}) {
		t.Fatal("expected Binance not to support forex")
	}
}

func TestBinanceProviderGetCandles(t *testing.T) {
	open := time.Now().UTC().Add(-2 * time.Hour).UnixMilli()
	close := time.Now().UTC().Add(-time.Hour).UnixMilli()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([][]any{
			{open, "100", "110", "90", "105", "12.5", close, "0", 1, "0", "0", "0"},
		})
	}))
	defer server.Close()

	client, err := feed.NewBinanceClient(feed.BinanceConfig{
		Symbols: []string{"BTCUSDT"}, Interval: "1h", BaseURL: server.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	p := NewBinanceProvider(client)
	candles, err := p.GetCandles(context.Background(), manager.CandleRequest{
		Symbol: "BTCUSDT", Timeframe: manager.Timeframe1h,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(candles) != 1 {
		t.Fatalf("expected one candle, got %d", len(candles))
	}
	if candles[0].Provider != "binance" || candles[0].Timeframe != manager.Timeframe1h || candles[0].Close != 105 {
		t.Fatalf("unexpected normalized candle: %+v", candles[0])
	}
}

func TestBinanceProviderMappingsAndErrors(t *testing.T) {
	if timeframeToBinance(manager.Timeframe4h) != "4h" {
		t.Fatal("expected 4h Binance mapping")
	}
	if timeframeToBinance(manager.Timeframe30m) != "30m" {
		t.Fatal("expected 30m Binance mapping")
	}
	if timeframeToBinance(manager.Timeframe("2h")) != "" {
		t.Fatal("unexpected unsupported timeframe mapping")
	}
	classified := classifyBinanceError(errMsg("status 429: too many requests"))
	providerErr, ok := classified.(*manager.ProviderError)
	if !ok || !providerErr.IsRateLimited() || !providerErr.IsRetryable() {
		t.Fatalf("unexpected classified error: %v", classified)
	}
}
