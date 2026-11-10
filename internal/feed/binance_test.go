package feed

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestBinanceConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     BinanceConfig
		wantErr bool
	}{
		{"valid without API key", BinanceConfig{Symbols: []string{"BTCUSDT"}, Interval: "1h"}, false},
		{"missing symbols", BinanceConfig{Interval: "1h"}, true},
		{"bad interval", BinanceConfig{Symbols: []string{"BTCUSDT"}, Interval: "10m"}, true},
		{"limit too large", BinanceConfig{Symbols: []string{"BTCUSDT"}, Interval: "1h", Limit: 1001}, true},
		// Fail closed on an instrument with no verified tick size: a guessed
		// scale changes the meaning of every tick-denominated parameter.
		{"unverified instrument", BinanceConfig{Symbols: []string{"DOGEUSDT"}, Interval: "1h"}, true},
	}
	for _, tt := range tests {
		if err := tt.cfg.Validate(); (err != nil) != tt.wantErr {
			t.Errorf("%s: Validate() error = %v, wantErr %v", tt.name, err, tt.wantErr)
		}
	}
}

func TestBinanceSymbol(t *testing.T) {
	for input, want := range map[string]string{
		"BTCUSDT":  "BTCUSDT",
		"btc/usdt": "BTCUSDT",
		"eth-usdt": "ETHUSDT",
		"sol_usdt": "SOLUSDT",
	} {
		if got := binanceSymbol(input); got != want {
			t.Errorf("binanceSymbol(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestBinanceImplementsSource(t *testing.T) {
	var _ Source = (*Binance)(nil)
}

// The forming kline must be dropped and each closed bar emitted exactly once.
func TestBinancePollFiltersOpenKlineAndDeduplicates(t *testing.T) {
	now := time.Now().UTC()
	closedOpen := now.Add(-2 * time.Hour).UnixMilli()
	closedClose := now.Add(-time.Hour).UnixMilli()
	openOpen := now.Add(time.Hour).UnixMilli()
	openClose := now.Add(2 * time.Hour).UnixMilli()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/klines" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("symbol"); got != "BTCUSDT" {
			t.Errorf("unexpected symbol: %q", got)
		}
		if got := r.URL.Query().Get("interval"); got != "1h" {
			t.Errorf("unexpected interval: %q", got)
		}
		_ = json.NewEncoder(w).Encode([][]any{
			{closedOpen, "100.00", "110.00", "90.00", "105.00", "12.5", closedClose, "0", 1, "0", "0", "0"},
			{openOpen, "105.00", "115.00", "95.00", "110.00", "7.5", openClose, "0", 1, "0", "0", "0"},
		})
	}))
	defer server.Close()

	source, err := NewBinance(BinanceConfig{
		Symbols: []string{"BTCUSDT"}, Interval: "1h", Limit: 5, BaseURL: server.URL,
	})
	if err != nil {
		t.Fatal(err)
	}

	candles, err := source.Poll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(candles) != 1 {
		t.Fatalf("expected one closed candle, got %d", len(candles))
	}
	candle := candles[0]
	if candle.Symbol != "BTCUSDT" || candle.Source != "binance" {
		t.Errorf("unexpected provenance: %s/%s", candle.Source, candle.Symbol)
	}
	// BTCUSDT is verified at 2 decimals, so 105.00 is 10500 integer ticks.
	if candle.Open != 10000 || candle.High != 11000 || candle.Low != 9000 || candle.Close != 10500 {
		t.Errorf("unexpected OHLC ticks: %+v", candle)
	}
	// The owned interval is derived from the open, because Binance reports a
	// close one millisecond early (23:59:59.999 for a bar owning up to 24:00).
	if !candle.CloseTime.Equal(candle.OpenTime.Add(time.Hour)) {
		t.Errorf("CloseTime %s is not open plus one hour", candle.CloseTime)
	}
	if candle.Timeframe != time.Hour {
		t.Errorf("Timeframe = %s, want 1h", candle.Timeframe)
	}

	candles, err = source.Poll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(candles) != 0 {
		t.Fatalf("expected deduplicated second poll, got %d candles", len(candles))
	}
}

func TestDefaultBinanceSymbols(t *testing.T) {
	symbols := DefaultBinanceSymbols()
	if len(symbols) == 0 || symbols[0] != "BTCUSDT" {
		t.Fatalf("unexpected default Binance symbols: %v", symbols)
	}
}
