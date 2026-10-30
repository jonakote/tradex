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

func TestBinanceFetchCandlesFiltersOpenKlineAndDeduplicates(t *testing.T) {
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
		json.NewEncoder(w).Encode([][]any{
			{closedOpen, "100", "110", "90", "105", "12.5", closedClose, "0", 1, "0", "0", "0"},
			{openOpen, "105", "115", "95", "110", "7.5", openClose, "0", 1, "0", "0", "0"},
		})
	}))
	defer server.Close()

	client, err := NewBinanceClient(BinanceConfig{
		Symbols: []string{"BTCUSDT"}, Interval: "1h", Limit: 5, BaseURL: server.URL,
	})
	if err != nil {
		t.Fatal(err)
	}

	candles, err := client.FetchCandles(context.Background(), "BTC/USDT")
	if err != nil {
		t.Fatal(err)
	}
	if len(candles) != 1 {
		t.Fatalf("expected one closed candle, got %d", len(candles))
	}
	if candles[0].Symbol != "BTC/USDT" || candles[0].Close != 105 || candles[0].Volume != 12.5 {
		t.Fatalf("unexpected candle: %+v", candles[0])
	}

	candles, err = client.FetchCandles(context.Background(), "BTC/USDT")
	if err != nil {
		t.Fatal(err)
	}
	if len(candles) != 0 {
		t.Fatalf("expected deduplicated second fetch, got %d candles", len(candles))
	}
}

func TestBinanceFetchQuote(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/ticker/bookTicker" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("symbol"); got != "ETHUSDT" {
			t.Errorf("unexpected symbol: %q", got)
		}
		json.NewEncoder(w).Encode(map[string]string{"bidPrice": "2000.00", "askPrice": "2000.50"})
	}))
	defer server.Close()

	client, err := NewBinanceClient(BinanceConfig{Symbols: []string{"ETHUSDT"}, Interval: "1h", BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	quote, err := client.FetchQuote(context.Background(), "ETHUSDT")
	if err != nil {
		t.Fatal(err)
	}
	if quote.Bid != 2000 || quote.Ask != 2000.5 || quote.Price != 2000.25 {
		t.Fatalf("unexpected quote: %+v", quote)
	}
}

func TestDefaultBinanceSymbols(t *testing.T) {
	symbols := DefaultBinanceSymbols()
	if len(symbols) == 0 || symbols[0] != "BTCUSDT" {
		t.Fatalf("unexpected default Binance symbols: %v", symbols)
	}
}
