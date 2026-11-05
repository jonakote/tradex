package manager

import (
	"context"
	"testing"
	"time"
)

func TestTimeframeValid(t *testing.T) {
	valid := []Timeframe{Timeframe1m, Timeframe5m, Timeframe15m, Timeframe30m, Timeframe1h, Timeframe4h, Timeframe1d, Timeframe1w}
	for _, tf := range valid {
		if !tf.Valid() {
			t.Errorf("%s should be valid", tf)
		}
		if tf.Duration() == 0 {
			t.Errorf("%s duration should not be zero", tf)
		}
	}
	if Timeframe("3h").Valid() {
		t.Error("3h should be invalid")
	}
}

func TestInstrumentValidation(t *testing.T) {
	valid := Instrument{Symbol: "EUR/USD", Type: InstrumentForex}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid instrument rejected: %v", err)
	}

	if err := (Instrument{Symbol: "", Type: InstrumentForex}).Validate(); err == nil {
		t.Fatal("accepted empty symbol")
	}
	if err := (Instrument{Symbol: "X", Type: "invalid"}).Validate(); err == nil {
		t.Fatal("accepted invalid type")
	}
}

func TestCandleValidation(t *testing.T) {
	valid := Candle{
		Symbol: "EUR/USD", Timeframe: Timeframe1h,
		OpenTime: time.Date(2026, 8, 20, 14, 0, 0, 0, time.UTC),
		Open:     1.08400, High: 1.08500, Low: 1.08300, Close: 1.08450,
		Provider: "twelvedata", FetchedAt: time.Now(),
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
	t.Run("invalid timeframe", func(t *testing.T) {
		c := valid
		c.Timeframe = "bad"
		if err := c.Validate(); err == nil {
			t.Fatal("accepted")
		}
	})
	t.Run("zero open time", func(t *testing.T) {
		c := valid
		c.OpenTime = time.Time{}
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
	t.Run("bad geometry", func(t *testing.T) {
		c := valid
		c.High = 1.08000 // below low
		if err := c.Validate(); err == nil {
			t.Fatal("accepted")
		}
	})
	t.Run("missing provider", func(t *testing.T) {
		c := valid
		c.Provider = ""
		if err := c.Validate(); err == nil {
			t.Fatal("accepted")
		}
	})
}

func TestCandleRequestValidation(t *testing.T) {
	valid := CandleRequest{Symbol: "EUR/USD", Timeframe: Timeframe1h, Limit: 5}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid rejected: %v", err)
	}

	if err := (CandleRequest{Symbol: "", Timeframe: Timeframe1h}).Validate(); err == nil {
		t.Fatal("accepted empty symbol")
	}
	if err := (CandleRequest{Symbol: "X", Timeframe: "bad"}).Validate(); err == nil {
		t.Fatal("accepted bad timeframe")
	}

	now := time.Now()
	bad := CandleRequest{Symbol: "X", Timeframe: Timeframe1h, From: now, To: now.Add(-time.Hour)}
	if err := bad.Validate(); err == nil {
		t.Fatal("accepted to before from")
	}
}

func TestCandleRequestCacheKey(t *testing.T) {
	r1 := CandleRequest{Symbol: "EUR/USD", Timeframe: Timeframe1h, Limit: 5}
	r2 := CandleRequest{Symbol: "EUR/USD", Timeframe: Timeframe1h, Limit: 5}
	if r1.CacheKey() != r2.CacheKey() {
		t.Fatal("identical requests should have same cache key")
	}

	r3 := CandleRequest{Symbol: "GBP/USD", Timeframe: Timeframe1h, Limit: 5}
	if r1.CacheKey() == r3.CacheKey() {
		t.Fatal("different symbols should have different cache keys")
	}
}

func TestQuoteAge(t *testing.T) {
	now := time.Now()
	q := Quote{FetchedAt: now.Add(-5 * time.Second)}
	age := q.Age(now)
	if age < 4*time.Second || age > 6*time.Second {
		t.Fatalf("unexpected age: %v", age)
	}
}

func TestProviderError(t *testing.T) {
	err := &ProviderError{Provider: "twelvedata", StatusCode: 429, Message: "rate limited", Retryable: true, RetryAfter: 60 * time.Second}
	if !err.IsRateLimited() {
		t.Fatal("should be rate limited")
	}
	if !err.IsRetryable() {
		t.Fatal("should be retryable")
	}
	if err.Error() == "" {
		t.Fatal("error string should not be empty")
	}
}

// fakeProvider implements MarketDataProvider for testing.
type fakeProvider struct {
	name      string
	supported map[string]bool
	candles   []Candle
	quote     Quote
	err       error
	quota     ProviderQuota
	callCount int
}

func (f *fakeProvider) Name() string { return f.name }
func (f *fakeProvider) Supports(i Instrument) bool {
	return f.supported[i.Symbol]
}
func (f *fakeProvider) GetCandles(_ context.Context, _ CandleRequest) ([]Candle, error) {
	f.callCount++
	if f.err != nil {
		return nil, f.err
	}
	return f.candles, nil
}
func (f *fakeProvider) GetQuote(_ context.Context, _ QuoteRequest) (Quote, error) {
	f.callCount++
	if f.err != nil {
		return Quote{}, f.err
	}
	return f.quote, nil
}
func (f *fakeProvider) RemainingQuota() ProviderQuota { return f.quota }

var _ MarketDataProvider = (*fakeProvider)(nil)
