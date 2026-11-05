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

func TestTwelveDataProviderSupports(t *testing.T) {
	client, _ := feed.NewTwelveDataClient(feed.TwelveDataConfig{
		APIKey: "test", Symbols: []string{"EUR/USD"}, Interval: "1h",
	})
	p := NewTwelveDataProvider(client, feed.TwelveDataConfig{})

	if p.Name() != "twelvedata" {
		t.Fatalf("expected 'twelvedata', got %q", p.Name())
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

func TestTwelveDataProviderGetCandles(t *testing.T) {
	type twelveResp struct {
		Meta   interface{} `json:"meta"`
		Values []struct {
			Datetime string `json:"datetime"`
			Open     string `json:"open"`
			High     string `json:"high"`
			Low      string `json:"low"`
			Close    string `json:"close"`
			Volume   string `json:"volume"`
		} `json:"values"`
		Status string `json:"status"`
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := twelveResp{
			Status: "ok",
			Values: []struct {
				Datetime string `json:"datetime"`
				Open     string `json:"open"`
				High     string `json:"high"`
				Low      string `json:"low"`
				Close    string `json:"close"`
				Volume   string `json:"volume"`
			}{
				{Datetime: "2026-08-20 14:00:00", Open: "1.08400", High: "1.08500", Low: "1.08300", Close: "1.08450", Volume: "5000"},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client, _ := feed.NewTwelveDataClient(feed.TwelveDataConfig{
		APIKey: "test", Symbols: []string{"EUR/USD"}, Interval: "1h", BaseURL: server.URL,
	})
	p := NewTwelveDataProvider(client, feed.TwelveDataConfig{})

	candles, err := p.GetCandles(context.Background(), manager.CandleRequest{
		Symbol: "EUR/USD", Timeframe: manager.Timeframe1h, Limit: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(candles) != 1 {
		t.Fatalf("expected 1 candle, got %d", len(candles))
	}
	if candles[0].Provider != "twelvedata" {
		t.Fatalf("expected provider 'twelvedata', got %q", candles[0].Provider)
	}
	if candles[0].Close != 1.08450 {
		t.Fatalf("unexpected close: %f", candles[0].Close)
	}
}

func TestTwelveDataProviderQuota(t *testing.T) {
	client, _ := feed.NewTwelveDataClient(feed.TwelveDataConfig{
		APIKey: "test", Symbols: []string{"EUR/USD"}, Interval: "1h",
	})
	p := NewTwelveDataProvider(client, feed.TwelveDataConfig{})

	q := p.RemainingQuota()
	if q.Provider != "twelvedata" {
		t.Fatal("wrong provider name")
	}
	if q.DailyLimit != 800 {
		t.Fatalf("expected 800 daily limit, got %d", q.DailyLimit)
	}
	if q.CreditsRemaining != 800 {
		t.Fatalf("expected 800 remaining initially, got %d", q.CreditsRemaining)
	}
}

func TestFCSProviderSupports(t *testing.T) {
	client, _ := feed.NewFCSClient(feed.FCSConfig{
		APIKey: "test", Symbols: []string{"XAG/USD"}, Interval: "1h",
	})
	p := NewFCSProvider(client, feed.FCSConfig{})

	if p.Name() != "fcs" {
		t.Fatalf("expected 'fcs', got %q", p.Name())
	}

	// FCS supports everything
	tests := []manager.Instrument{
		{Symbol: "EUR/USD", Type: manager.InstrumentForex},
		{Symbol: "XAG/USD", Type: manager.InstrumentMetal},
		{Symbol: "BRENT", Type: manager.InstrumentCommodity},
		{Symbol: "US100", Type: manager.InstrumentIndex},
		{Symbol: "AAPL", Type: manager.InstrumentStock},
	}
	for _, inst := range tests {
		if !p.Supports(inst) {
			t.Errorf("FCS should support %s/%s", inst.Symbol, inst.Type)
		}
	}
}

func TestFCSProviderGetCandles(t *testing.T) {
	type fcsResp struct {
		Status   bool `json:"status"`
		Code     int  `json:"code"`
		Response []struct {
			O  string `json:"o"`
			H  string `json:"h"`
			L  string `json:"l"`
			C  string `json:"c"`
			TM string `json:"tm"`
		} `json:"response"`
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := fcsResp{
			Status: true,
			Code:   200,
			Response: []struct {
				O  string `json:"o"`
				H  string `json:"h"`
				L  string `json:"l"`
				C  string `json:"c"`
				TM string `json:"tm"`
			}{
				{O: "23.50", H: "23.80", L: "23.40", C: "23.70", TM: "2026-08-20 14:00:00"},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client, _ := feed.NewFCSClient(feed.FCSConfig{
		APIKey: "test", Symbols: []string{"XAG/USD"}, Interval: "1h", BaseURL: server.URL,
	})
	p := NewFCSProvider(client, feed.FCSConfig{})

	candles, err := p.GetCandles(context.Background(), manager.CandleRequest{
		Symbol: "XAG/USD", Timeframe: manager.Timeframe1h, Limit: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(candles) != 1 {
		t.Fatalf("expected 1 candle, got %d", len(candles))
	}
	if candles[0].Provider != "fcs" {
		t.Fatalf("expected provider 'fcs', got %q", candles[0].Provider)
	}
	if candles[0].Close != 23.70 {
		t.Fatalf("unexpected close: %f", candles[0].Close)
	}
}

func TestFCSProviderQuota(t *testing.T) {
	client, _ := feed.NewFCSClient(feed.FCSConfig{
		APIKey: "test", Symbols: []string{"XAG/USD"}, Interval: "1h",
	})
	p := NewFCSProvider(client, feed.FCSConfig{})

	q := p.RemainingQuota()
	if q.Provider != "fcs" || q.CreditsRemaining != 500 {
		t.Fatalf("unexpected quota: %+v", q)
	}
}

func TestClassifyTwelveDataError(t *testing.T) {
	err429 := classifyTwelveDataError(errMsg("status 429: run out of API credits"))
	pe := err429.(*manager.ProviderError)
	if !pe.IsRateLimited() || !pe.IsRetryable() {
		t.Fatal("429 should be rate limited and retryable")
	}

	err404 := classifyTwelveDataError(errMsg("status 404: not available"))
	pe = err404.(*manager.ProviderError)
	if pe.IsRateLimited() || pe.IsRetryable() {
		t.Fatal("404 should not be retryable")
	}
}

func TestClassifyFCSError(t *testing.T) {
	err401 := classifyFCSError(errMsg("401 Invalid API key"))
	pe := err401.(*manager.ProviderError)
	if pe.IsRetryable() {
		t.Fatal("401 should not be retryable")
	}
}

func TestTimeframeConversions(t *testing.T) {
	if timeframeToTwelveData(manager.Timeframe1h) != "1h" {
		t.Fatal("bad twelvedata 1h")
	}
	if timeframeToTwelveData(manager.Timeframe15m) != "15min" {
		t.Fatal("bad twelvedata 15m")
	}
	if timeframeToFCS(manager.Timeframe1h) != "1h" {
		t.Fatal("bad fcs 1h")
	}
	if timeframeToFCS(manager.Timeframe1m) != "" {
		t.Fatal("fcs shouldn't support 1m")
	}
}

// helper
type errMsg string

func (e errMsg) Error() string { return string(e) }

// Ensure time is imported
var _ = time.Now
