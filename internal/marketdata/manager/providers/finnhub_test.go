package providers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"tradex/internal/feed"
	"tradex/internal/marketdata/manager"
)

func TestFinnhubProviderSupports(t *testing.T) {
	client, _ := feed.NewFinnhubClient(feed.FinnhubConfig{
		APIKey: "test", Symbols: []string{"EUR/USD"}, Interval: "1h",
	})
	p := NewFinnhubProvider(client, feed.FinnhubConfig{})

	if p.Name() != "finnhub" {
		t.Fatalf("expected 'finnhub', got %q", p.Name())
	}

	tests := []struct {
		inst   manager.Instrument
		expect bool
	}{
		{manager.Instrument{Symbol: "EUR/USD", Type: manager.InstrumentForex}, true},
		{manager.Instrument{Symbol: "XAU/USD", Type: manager.InstrumentMetal}, true},
		{manager.Instrument{Symbol: "XAG/USD", Type: manager.InstrumentMetal}, false},
		{manager.Instrument{Symbol: "QQQ", Type: manager.InstrumentStock}, true},
		{manager.Instrument{Symbol: "BRENT", Type: manager.InstrumentCommodity}, false},
		{manager.Instrument{Symbol: "US100", Type: manager.InstrumentIndex}, false},
	}
	for _, tt := range tests {
		got := p.Supports(tt.inst)
		if got != tt.expect {
			t.Errorf("Supports(%s/%s) = %v, want %v", tt.inst.Symbol, tt.inst.Type, got, tt.expect)
		}
	}
}

func TestFinnhubProviderGetCandles(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]interface{}{
			"s": "ok",
			"t": []int64{1755542400},
			"o": []float64{1.0840},
			"h": []float64{1.0850},
			"l": []float64{1.0830},
			"c": []float64{1.0845},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client, _ := feed.NewFinnhubClient(feed.FinnhubConfig{
		APIKey: "test", Symbols: []string{"EUR/USD"}, Interval: "1h", BaseURL: server.URL,
	})
	p := NewFinnhubProvider(client, feed.FinnhubConfig{})

	candles, err := p.GetCandles(context.Background(), manager.CandleRequest{
		Symbol: "EUR/USD", Timeframe: manager.Timeframe1h, Limit: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(candles) != 1 {
		t.Fatalf("expected 1 candle, got %d", len(candles))
	}
	if candles[0].Provider != "finnhub" {
		t.Fatalf("expected provider 'finnhub', got %q", candles[0].Provider)
	}
	if candles[0].Close != 1.0845 {
		t.Fatalf("unexpected close: %f", candles[0].Close)
	}
}

func TestFinnhubProviderUsesRequestedTimeframe(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("resolution"); got != "15" {
			t.Fatalf("expected 15-minute resolution, got %q", got)
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"s": "ok", "t": []int64{1755542400}, "o": []float64{1}, "h": []float64{2},
			"l": []float64{0.5}, "c": []float64{1.5},
		})
	}))
	defer server.Close()

	client, _ := feed.NewFinnhubClient(feed.FinnhubConfig{
		APIKey: "test", Symbols: []string{"EUR/USD"}, Interval: "1h", BaseURL: server.URL,
	})
	provider := NewFinnhubProvider(client, feed.FinnhubConfig{})
	candles, err := provider.GetCandles(context.Background(), manager.CandleRequest{
		Symbol: "EUR/USD", Timeframe: manager.Timeframe15m, Limit: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(candles) != 1 || candles[0].Timeframe != manager.Timeframe15m {
		t.Fatalf("unexpected candles: %+v", candles)
	}
}

func TestFinnhubProviderQuota(t *testing.T) {
	client, _ := feed.NewFinnhubClient(feed.FinnhubConfig{
		APIKey: "test", Symbols: []string{"EUR/USD"}, Interval: "1h",
	})
	p := NewFinnhubProvider(client, feed.FinnhubConfig{})

	q := p.RemainingQuota()
	if q.Provider != "finnhub" {
		t.Fatal("wrong provider name")
	}
	if q.DailyLimit != 86400 {
		t.Fatalf("expected 86400 daily limit, got %d", q.DailyLimit)
	}
	if q.MinuteLimit != 60 {
		t.Fatalf("expected 60 minute limit, got %d", q.MinuteLimit)
	}
	if q.CreditsRemaining != 86400 {
		t.Fatalf("expected full quota initially, got %d", q.CreditsRemaining)
	}
}

func TestClassifyFinnhubError(t *testing.T) {
	err429 := classifyFinnhubError(errMsg("finnhub EUR/USD: rate limited (429)"))
	pe := err429.(*manager.ProviderError)
	if !pe.IsRateLimited() || !pe.IsRetryable() {
		t.Fatal("429 should be rate limited and retryable")
	}

	err401 := classifyFinnhubError(errMsg("finnhub EUR/USD: status 401"))
	pe = err401.(*manager.ProviderError)
	if pe.IsRetryable() {
		t.Fatal("401 should not be retryable")
	}
}

func TestTimeframeToFinnhub(t *testing.T) {
	if timeframeToFinnhub(manager.Timeframe1h) != "1h" {
		t.Fatal("bad finnhub 1h")
	}
	if timeframeToFinnhub(manager.Timeframe15m) != "15min" {
		t.Fatal("bad finnhub 15m")
	}
	if timeframeToFinnhub(manager.Timeframe4h) != "" {
		t.Fatal("finnhub shouldn't support 4h")
	}
}
