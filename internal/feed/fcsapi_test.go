package feed

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFCSConfigValidation(t *testing.T) {
	valid := FCSConfig{APIKey: "key", Symbols: []string{"XAG/USD"}, Interval: "1h"}
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

func TestFCSFetchCandlesFromFakeServer(t *testing.T) {
	rawCandles, _ := json.Marshal([]fcsCandle{
		{Open: "23.50", High: "23.80", Low: "23.40", Close: "23.70", TM: "2026-08-20 14:00:00"},
		{Open: "23.30", High: "23.55", Low: "23.20", Close: "23.50", TM: "2026-08-20 13:00:00"},
	})
	response := fcsResponse{
		Status:   true,
		Code:     200,
		Response: rawCandles,
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("symbol") != "XAGUSD" {
			t.Errorf("expected XAGUSD (no slash), got %s", q.Get("symbol"))
		}
		if q.Get("period") != "1h" {
			t.Errorf("unexpected period: %s", q.Get("period"))
		}
		if q.Get("access_key") != "test-key" {
			t.Errorf("unexpected key: %s", q.Get("access_key"))
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	client, err := NewFCSClient(FCSConfig{
		APIKey:   "test-key",
		Symbols:  []string{"XAG/USD"},
		Interval: "1h",
		BaseURL:  server.URL,
	})
	if err != nil {
		t.Fatal(err)
	}

	candles, err := client.FetchCandles(context.Background(), "XAG/USD")
	if err != nil {
		t.Fatal(err)
	}
	if len(candles) != 2 {
		t.Fatalf("expected 2 candles, got %d", len(candles))
	}
	if candles[0].Symbol != "XAG/USD" {
		t.Fatalf("unexpected symbol: %s", candles[0].Symbol)
	}
	if candles[0].Close != 23.70 && candles[1].Close != 23.70 {
		t.Fatalf("unexpected close values: %f, %f", candles[0].Close, candles[1].Close)
	}
}

func TestFCSDeduplication(t *testing.T) {
	rawCandles, _ := json.Marshal([]fcsCandle{
		{Open: "85.50", High: "86.00", Low: "85.00", Close: "85.80", TM: "2026-08-20 14:00:00"},
	})
	response := fcsResponse{
		Status:   true,
		Code:     200,
		Response: rawCandles,
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	client, _ := NewFCSClient(FCSConfig{
		APIKey:  "key",
		Symbols: []string{"BRENT"},
		Interval: "1h",
		BaseURL: server.URL,
	})

	candles, _ := client.FetchCandles(context.Background(), "BRENT")
	if len(candles) != 1 {
		t.Fatalf("first fetch: expected 1, got %d", len(candles))
	}

	candles, _ = client.FetchCandles(context.Background(), "BRENT")
	if len(candles) != 0 {
		t.Fatalf("second fetch: expected 0 new, got %d", len(candles))
	}
}

func TestFCSAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(fcsResponse{Status: false, Code: 401, Msg: "Invalid API key"})
	}))
	defer server.Close()

	client, _ := NewFCSClient(FCSConfig{
		APIKey:  "bad-key",
		Symbols: []string{"EURUSD"},
		Interval: "1h",
		BaseURL: server.URL,
	})

	_, err := client.FetchCandles(context.Background(), "EURUSD")
	if err == nil {
		t.Fatal("expected error on API error response")
	}
}

func TestParseFCSTime(t *testing.T) {
	// Datetime format
	tm := parseFCSTime("2026-08-20 14:00:00", "")
	if tm.IsZero() || tm.Hour() != 14 {
		t.Fatalf("datetime parse failed: %v", tm)
	}

	// Unix timestamp
	tm = parseFCSTime("", "1724155200")
	if tm.IsZero() {
		t.Fatal("unix parse failed")
	}

	// Invalid
	tm = parseFCSTime("", "")
	if !tm.IsZero() {
		t.Fatal("empty should return zero")
	}
}

func TestValidFCSInterval(t *testing.T) {
	valid := []string{"5m", "15m", "30m", "1h", "4h", "1d", "1w"}
	for _, v := range valid {
		if !validFCSInterval(v) {
			t.Errorf("%s should be valid", v)
		}
	}
	if validFCSInterval("1min") {
		t.Error("1min should not be valid for FCS")
	}
}

func TestDefaultFCSSymbols(t *testing.T) {
	symbols := DefaultFCSSymbols()
	if len(symbols) < 1 {
		t.Fatalf("expected at least 1, got %d", len(symbols))
	}
}

func TestFCSFetchCandlesObjectMapResponse(t *testing.T) {
	// FCS API often returns response as a map keyed by timestamp
	jsonPayload := `{
		"status": true,
		"code": 200,
		"msg": "Successfully",
		"response": {
			"1787022000": {"o": 65.36, "h": 65.50, "l": 65.15, "c": 65.20, "tm": "2026-08-20 14:00:00"},
			"1787025600": {"o": "65.20", "h": "65.25", "l": "65.07", "c": "65.15", "t": 1787025600}
		}
	}`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(jsonPayload))
	}))
	defer server.Close()

	client, err := NewFCSClient(FCSConfig{
		APIKey:   "test-key",
		Symbols:  []string{"XAG/USD"},
		Interval: "1h",
		BaseURL:  server.URL,
	})
	if err != nil {
		t.Fatal(err)
	}

	candles, err := client.FetchCandles(context.Background(), "XAG/USD")
	if err != nil {
		t.Fatalf("failed to parse object map response: %v", err)
	}
	if len(candles) != 2 {
		t.Fatalf("expected 2 candles, got %d", len(candles))
	}
}
