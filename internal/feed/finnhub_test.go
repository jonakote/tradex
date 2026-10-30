package feed

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFinnhubConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     FinnhubConfig
		wantErr bool
	}{
		{"valid", FinnhubConfig{APIKey: "k", Symbols: []string{"EUR/USD"}, Interval: "1h"}, false},
		{"missing key", FinnhubConfig{Symbols: []string{"EUR/USD"}, Interval: "1h"}, true},
		{"missing symbols", FinnhubConfig{APIKey: "k", Interval: "1h"}, true},
		{"bad interval", FinnhubConfig{APIKey: "k", Symbols: []string{"EUR/USD"}, Interval: "2h"}, true},
	}
	for _, tt := range tests {
		err := tt.cfg.Validate()
		if (err != nil) != tt.wantErr {
			t.Errorf("%s: Validate() error = %v, wantErr %v", tt.name, err, tt.wantErr)
		}
	}
}

func TestFinnhubSymbolConversion(t *testing.T) {
	tests := []struct {
		symbol   string
		exchange string
		want     string
	}{
		{"EUR/USD", "OANDA", "OANDA:EUR_USD"},
		{"XAU/USD", "OANDA", "OANDA:XAU_USD"},
		{"QQQ", "OANDA", "QQQ"},
	}
	for _, tt := range tests {
		got := finnhubSymbol(tt.symbol, tt.exchange)
		if got != tt.want {
			t.Errorf("finnhubSymbol(%q) = %q, want %q", tt.symbol, got, tt.want)
		}
	}
}

func TestFinnhubResolution(t *testing.T) {
	tests := map[string]string{
		"1min": "1", "5min": "5", "15min": "15", "30min": "30", "1h": "60", "1day": "D",
	}
	for interval, want := range tests {
		if got := finnhubResolution(interval); got != want {
			t.Errorf("finnhubResolution(%q) = %q, want %q", interval, got, want)
		}
	}
}

func TestFinnhubFetchCandles(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("symbol"); got != "OANDA:EUR_USD" {
			t.Errorf("unexpected symbol param: %q", got)
		}
		if got := r.URL.Query().Get("resolution"); got != "60" {
			t.Errorf("unexpected resolution param: %q", got)
		}
		resp := finnhubCandleResponse{
			Status: "ok",
			T:      []int64{1755542400, 1755546000},
			O:      []float64{1.0840, 1.0845},
			H:      []float64{1.0850, 1.0855},
			L:      []float64{1.0830, 1.0840},
			C:      []float64{1.0845, 1.0850},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client, err := NewFinnhubClient(FinnhubConfig{
		APIKey: "test", Symbols: []string{"EUR/USD"}, Interval: "1h", BaseURL: server.URL,
	})
	if err != nil {
		t.Fatal(err)
	}

	candles, err := client.FetchCandles(context.Background(), "EUR/USD")
	if err != nil {
		t.Fatal(err)
	}
	if len(candles) != 2 {
		t.Fatalf("expected 2 candles, got %d", len(candles))
	}
	if candles[0].Close != 1.0845 || candles[1].Close != 1.0850 {
		t.Fatalf("unexpected closes: %f, %f", candles[0].Close, candles[1].Close)
	}

	// Second fetch should deduplicate to zero new candles
	candles, err = client.FetchCandles(context.Background(), "EUR/USD")
	if err != nil {
		t.Fatal(err)
	}
	if len(candles) != 0 {
		t.Fatalf("expected 0 new candles after dedup, got %d", len(candles))
	}
}

func TestFinnhubFetchCandlesAtUsesRequestedIntervalAndKeepsIndependentDeduplication(t *testing.T) {
	var resolutions []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resolutions = append(resolutions, r.URL.Query().Get("resolution"))
		json.NewEncoder(w).Encode(finnhubCandleResponse{
			Status: "ok", T: []int64{1755542400}, O: []float64{1}, H: []float64{2},
			L: []float64{0.5}, C: []float64{1.5},
		})
	}))
	defer server.Close()

	client, err := NewFinnhubClient(FinnhubConfig{
		APIKey: "test", Symbols: []string{"EUR/USD"}, Interval: "1h", BaseURL: server.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.FetchCandlesAt(context.Background(), "EUR/USD", "15min"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.FetchCandlesAt(context.Background(), "EUR/USD", "1h"); err != nil {
		t.Fatal(err)
	}
	if len(resolutions) != 2 || resolutions[0] != "15" || resolutions[1] != "60" {
		t.Fatalf("unexpected Finnhub resolutions: %v", resolutions)
	}
}

func TestFinnhubFetchCandlesNoData(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(finnhubCandleResponse{Status: "no_data"})
	}))
	defer server.Close()

	client, _ := NewFinnhubClient(FinnhubConfig{
		APIKey: "test", Symbols: []string{"EUR/USD"}, Interval: "1h", BaseURL: server.URL,
	})

	if _, err := client.FetchCandles(context.Background(), "EUR/USD"); err == nil {
		t.Fatal("expected error for no_data response")
	}
}

func TestFinnhubFetchCandlesRateLimited(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	client, _ := NewFinnhubClient(FinnhubConfig{
		APIKey: "test", Symbols: []string{"EUR/USD"}, Interval: "1h", BaseURL: server.URL,
	})

	if _, err := client.FetchCandles(context.Background(), "EUR/USD"); err == nil {
		t.Fatal("expected error for 429 response")
	}
}

func TestFinnhubFetchQuote(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]float64{
			"c": 480.25, "h": 482.0, "l": 478.5, "o": 479.0, "t": 1755542400,
		})
	}))
	defer server.Close()

	client, _ := NewFinnhubClient(FinnhubConfig{
		APIKey: "test", Symbols: []string{"QQQ"}, Interval: "1h", BaseURL: server.URL,
	})

	q, err := client.FetchQuote(context.Background(), "QQQ")
	if err != nil {
		t.Fatal(err)
	}
	if q.Price != 480.25 {
		t.Fatalf("unexpected price: %f", q.Price)
	}
}

func TestDefaultFinnhubSymbols(t *testing.T) {
	symbols := DefaultFinnhubSymbols()
	if len(symbols) == 0 {
		t.Fatal("expected non-empty default symbols")
	}
	for _, s := range symbols {
		if s == "" {
			t.Fatal("empty default symbol")
		}
	}
}
