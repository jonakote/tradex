package feed

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCandleValidation(t *testing.T) {
	valid := Candle{
		Symbol: "EUR/USD", Interval: "1h",
		Open: 1.08400, High: 1.08500, Low: 1.08300, Close: 1.08450,
		Volume: 1000, Time: time.Now(),
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid candle rejected: %v", err)
	}

	t.Run("missing symbol", func(t *testing.T) {
		c := valid
		c.Symbol = ""
		if err := c.Validate(); err == nil {
			t.Fatal("accepted")
		}
	})
	t.Run("negative price", func(t *testing.T) {
		c := valid
		c.Open = -1
		if err := c.Validate(); err == nil {
			t.Fatal("accepted")
		}
	})
	t.Run("high below low", func(t *testing.T) {
		c := valid
		c.High = 1.08200
		c.Low = 1.08300
		if err := c.Validate(); err == nil {
			t.Fatal("accepted")
		}
	})
	t.Run("zero time", func(t *testing.T) {
		c := valid
		c.Time = time.Time{}
		if err := c.Validate(); err == nil {
			t.Fatal("accepted")
		}
	})
}

func TestTwelveDataConfigValidation(t *testing.T) {
	valid := TwelveDataConfig{
		APIKey:   "test-key",
		Symbols:  []string{"EUR/USD"},
		Interval: "1h",
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}

	t.Run("missing key", func(t *testing.T) {
		c := valid
		c.APIKey = ""
		if err := c.Validate(); err == nil {
			t.Fatal("accepted")
		}
	})
	t.Run("no symbols", func(t *testing.T) {
		c := valid
		c.Symbols = nil
		if err := c.Validate(); err == nil {
			t.Fatal("accepted")
		}
	})
	t.Run("invalid interval", func(t *testing.T) {
		c := valid
		c.Interval = "3h"
		if err := c.Validate(); err == nil {
			t.Fatal("accepted")
		}
	})
}

func TestFetchCandlesFromFakeServer(t *testing.T) {
	response := twelveDataResponse{
		Meta: twelveDataMeta{Symbol: "EUR/USD", Interval: "1h"},
		Values: []twelveDataValue{
			{Datetime: "2026-08-20 14:00:00", Open: "1.08500", High: "1.08600", Low: "1.08400", Close: "1.08550", Volume: "5000"},
			{Datetime: "2026-08-20 13:00:00", Open: "1.08400", High: "1.08520", Low: "1.08350", Close: "1.08500", Volume: "4500"},
			{Datetime: "2026-08-20 12:00:00", Open: "1.08300", High: "1.08450", Low: "1.08250", Close: "1.08400", Volume: "4000"},
		},
		Status: "ok",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify query params
		q := r.URL.Query()
		if q.Get("symbol") != "EUR/USD" {
			t.Errorf("unexpected symbol: %s", q.Get("symbol"))
		}
		if q.Get("interval") != "1h" {
			t.Errorf("unexpected interval: %s", q.Get("interval"))
		}
		if q.Get("apikey") != "test-key" {
			t.Errorf("unexpected apikey: %s", q.Get("apikey"))
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	client, err := NewTwelveDataClient(TwelveDataConfig{
		APIKey:   "test-key",
		Symbols:  []string{"EUR/USD"},
		Interval: "1h",
		BaseURL:  server.URL,
	})
	if err != nil {
		t.Fatal(err)
	}

	candles, err := client.FetchCandles(context.Background(), "EUR/USD")
	if err != nil {
		t.Fatal(err)
	}
	if len(candles) != 3 {
		t.Fatalf("expected 3 candles, got %d", len(candles))
	}
	// Should be in chronological order (oldest first)
	if candles[0].Time.After(candles[1].Time) {
		t.Fatal("candles not in chronological order")
	}
	if candles[0].Close != 1.08400 {
		t.Fatalf("first candle close: %f", candles[0].Close)
	}
	if candles[2].Close != 1.08550 {
		t.Fatalf("last candle close: %f", candles[2].Close)
	}
}

func TestFetchCandlesDeduplication(t *testing.T) {
	callCount := 0
	response := twelveDataResponse{
		Meta: twelveDataMeta{Symbol: "XAU/USD", Interval: "15min"},
		Values: []twelveDataValue{
			{Datetime: "2026-08-20 14:15:00", Open: "1950.00", High: "1952.00", Low: "1949.00", Close: "1951.50", Volume: "100"},
			{Datetime: "2026-08-20 14:00:00", Open: "1948.00", High: "1950.50", Low: "1947.00", Close: "1950.00", Volume: "90"},
		},
		Status: "ok",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	client, _ := NewTwelveDataClient(TwelveDataConfig{
		APIKey:   "key",
		Symbols:  []string{"XAU/USD"},
		Interval: "15min",
		BaseURL:  server.URL,
	})

	// First fetch: get both candles
	candles, err := client.FetchCandles(context.Background(), "XAU/USD")
	if err != nil {
		t.Fatal(err)
	}
	if len(candles) != 2 {
		t.Fatalf("first fetch: expected 2, got %d", len(candles))
	}

	// Second fetch with same data: should return zero new candles
	candles, err = client.FetchCandles(context.Background(), "XAU/USD")
	if err != nil {
		t.Fatal(err)
	}
	if len(candles) != 0 {
		t.Fatalf("second fetch: expected 0 new, got %d", len(candles))
	}
}

func TestFetchCandlesAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(twelveDataResponse{
			Status: "error",
			Code:   429,
			Msg:    "Too many requests",
		})
	}))
	defer server.Close()

	client, _ := NewTwelveDataClient(TwelveDataConfig{
		APIKey:   "key",
		Symbols:  []string{"EUR/USD"},
		Interval: "1h",
		BaseURL:  server.URL,
	})

	_, err := client.FetchCandles(context.Background(), "EUR/USD")
	if err == nil {
		t.Fatal("expected error on API error response")
	}
}

func TestFetchCandlesHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("server error"))
	}))
	defer server.Close()

	client, _ := NewTwelveDataClient(TwelveDataConfig{
		APIKey:   "key",
		Symbols:  []string{"EUR/USD"},
		Interval: "1h",
		BaseURL:  server.URL,
	})

	_, err := client.FetchCandles(context.Background(), "EUR/USD")
	if err == nil {
		t.Fatal("expected error on HTTP 500")
	}
}

func TestParseTwelveDataTime(t *testing.T) {
	tests := []struct {
		input string
		valid bool
	}{
		{"2026-08-20 14:00:00", true},
		{"2026-08-20", true},
		{"invalid", false},
		{"", false},
	}
	for _, tt := range tests {
		result, err := parseTwelveDataTime(tt.input)
		if tt.valid && err != nil {
			t.Errorf("parseTwelveDataTime(%q) failed: %v", tt.input, err)
		}
		if !tt.valid && err == nil {
			t.Errorf("parseTwelveDataTime(%q) should have failed, got %v", tt.input, result)
		}
	}
}

func TestDefaultSymbols(t *testing.T) {
	symbols := DefaultSymbols()
	if len(symbols) < 8 {
		t.Fatalf("expected at least 8 default symbols, got %d", len(symbols))
	}
}

func TestValidInterval(t *testing.T) {
	valid := []string{"1min", "5min", "15min", "30min", "1h", "4h", "1day"}
	for _, v := range valid {
		if !validInterval(v) {
			t.Errorf("%s should be valid", v)
		}
	}
	invalid := []string{"3h", "10min", "2day", ""}
	for _, v := range invalid {
		if validInterval(v) {
			t.Errorf("%s should be invalid", v)
		}
	}
}
