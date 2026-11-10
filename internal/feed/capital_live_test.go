package feed

import (
	"context"
	"os"
	"testing"
	"time"

	"tradex/internal/instrument"
)

// Live smoke test against the real Capital.com API.
//
// Skipped unless TRADEX_LIVE_FEED_TEST=1, so the default suite stays
// hermetic and credential-free. Run it when changing the adapter:
//
//	set -a; . ./.env; set +a
//	TRADEX_LIVE_FEED_TEST=1 go test ./internal/feed -run Live -v
//
// It only reads market data. It must never be extended to place an order:
// the credential in use may belong to a live-money account.
func TestCapitalLiveSmoke(t *testing.T) {
	if os.Getenv("TRADEX_LIVE_FEED_TEST") != "1" {
		t.Skip("set TRADEX_LIVE_FEED_TEST=1 to run the live Capital.com smoke test")
	}
	config := CapitalConfig{
		APIKey:     os.Getenv("CAPITAL_API_KEY"),
		Identifier: os.Getenv("CAPITAL_IDENTIFIER"),
		Password:   os.Getenv("CAPITAL_PASSWORD"),
		BaseURL:    os.Getenv("CAPITAL_BASE_URL"),
		Symbols:    []string{"EURUSD", "XAUUSD"},
		Interval:   "1h",
		Bars:       10,
	}
	if config.APIKey == "" || config.Identifier == "" || config.Password == "" {
		t.Skip("CAPITAL_* credentials are not in the environment")
	}

	source, err := NewCapital(config)
	if err != nil {
		t.Fatalf("NewCapital: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	candles, err := source.Poll(ctx)
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(candles) == 0 {
		t.Fatal("Poll returned no candles")
	}
	now := time.Now().UTC()
	for _, candle := range candles {
		if err := candle.Validate(); err != nil {
			t.Errorf("invalid candle %+v: %v", candle, err)
		}
		if candle.CloseTime.After(now) {
			t.Errorf("%s bar at %s has not closed yet", candle.Symbol, candle.OpenTime)
		}
		if candle.Source != "capital.com" {
			t.Errorf("unexpected source %q", candle.Source)
		}
		spec, ok := instrument.Lookup(candle.Symbol)
		if !ok {
			t.Fatalf("%s has no verified precision", candle.Symbol)
		}
		// The integer tick must round-trip back to the venue's decimal
		// price exactly; a mismatch means the scale is wrong.
		if round := spec.Ticks(spec.Price(int64(candle.Close))); round != int64(candle.Close) {
			t.Errorf("%s close %d does not round-trip at %d digits",
				candle.Symbol, candle.Close, spec.Digits)
		}
	}
	t.Logf("polled %d closed candles across %d symbols", len(candles), len(config.Symbols))

	// A second poll must yield nothing new within the same bar.
	again, err := source.Poll(ctx)
	if err != nil {
		t.Fatalf("second Poll: %v", err)
	}
	if len(again) != 0 {
		t.Errorf("second Poll yielded %d candles, want 0", len(again))
	}

	// History must page a window wider than the venue's per-request cap and
	// return strictly ascending, gap-aware bars.
	to := now.Truncate(time.Hour)
	from := to.Add(-72 * time.Hour)
	history, err := source.History(ctx, "EURUSD", from, to)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(history) == 0 {
		t.Fatal("History returned no candles")
	}
	for i := 1; i < len(history); i++ {
		if !history[i].OpenTime.After(history[i-1].OpenTime) {
			t.Fatalf("History is not strictly ascending at %d: %s then %s",
				i, history[i-1].OpenTime, history[i].OpenTime)
		}
	}
	if first, last := history[0].OpenTime, history[len(history)-1].OpenTime; first.Before(from) || !last.Before(to) {
		t.Errorf("History returned bars outside [%s, %s): %s .. %s", from, to, first, last)
	}
	t.Logf("history: %d EURUSD h1 bars %s .. %s", len(history),
		history[0].OpenTime.Format(time.RFC3339), history[len(history)-1].OpenTime.Format(time.RFC3339))
}
