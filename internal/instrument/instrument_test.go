package instrument

import (
	"math"
	"testing"
)

// Precision is the load-bearing fact in this package: every price in tradex
// is an integer tick, so these values are pinned against the venues they were
// verified from. Changing one silently rescales every tick-denominated
// strategy parameter.
func TestVerifiedPrecision(t *testing.T) {
	cases := []struct {
		symbol string
		class  Class
		digits int
		scale  int64
		pip    float64
	}{
		// Verified against Dukascopy published integer points.
		{"EURUSD", ClassFX, 5, 100000, 0.0001},
		{"GBPUSD", ClassFX, 5, 100000, 0.0001},
		{"USDJPY", ClassFX, 3, 1000, 0.01},
		{"GBPJPY", ClassFX, 3, 1000, 0.01},
		{"XAUUSD", ClassMetal, 3, 1000, 0.1},
		{"XAGUSD", ClassMetal, 3, 1000, 0.01},
		// Verified against the Binance PRICE_FILTER tickSize.
		{"BTCUSDT", ClassCrypto, 2, 100, 0},
		{"ETHUSDT", ClassCrypto, 2, 100, 0},
		{"XRPUSDT", ClassCrypto, 4, 10000, 0},
		{"ADAUSDT", ClassCrypto, 4, 10000, 0},
	}
	for _, tc := range cases {
		t.Run(tc.symbol, func(t *testing.T) {
			spec, ok := Lookup(tc.symbol)
			if !ok {
				t.Fatal("not registered")
			}
			if spec.Class != tc.class {
				t.Fatalf("class: got %s want %s", spec.Class, tc.class)
			}
			if spec.Digits != tc.digits || spec.PriceScale != tc.scale {
				t.Fatalf("precision: got %d digits / scale %d, want %d / %d",
					spec.Digits, spec.PriceScale, tc.digits, tc.scale)
			}
			if spec.PipSize != tc.pip {
				t.Fatalf("pip: got %v want %v", spec.PipSize, tc.pip)
			}
			if spec.Unverified {
				t.Fatal("a venue-verified instrument must not be marked unverified")
			}
		})
	}
}

// The defect this package exists to prevent: BTCUSDT was treated as a 5-digit
// FX pair, making one tick $0.00001 and a 2-tick stop buffer worth $0.00002
// on an $86,000 instrument.
func TestCryptoTickSizeIsNotForexTickSize(t *testing.T) {
	btc, ok := Lookup("BTCUSDT")
	if !ok {
		t.Fatal("BTCUSDT must be registered")
	}
	if btc.TickSize() != 0.01 {
		t.Fatalf("BTCUSDT tick: got %v want 0.01", btc.TickSize())
	}
	// A 2-tick stop buffer must be worth 2 cents, not 2 hundred-thousandths.
	buffer := 2 * btc.TickSize()
	if math.Abs(buffer-0.02) > 1e-12 {
		t.Fatalf("2-tick buffer: got %v want 0.02", buffer)
	}
	// 86000.00 must round-trip exactly, with no float residue.
	ticks := btc.Ticks(86000.00)
	if ticks != 8600000 {
		t.Fatalf("ticks: got %d want 8600000", ticks)
	}
	if btc.Price(ticks) != 86000.00 {
		t.Fatalf("round trip: got %v", btc.Price(ticks))
	}
	// The old behaviour produced 85999.99998 here; the stop must now land on
	// a real tick boundary.
	stop := btc.Price(ticks - 2)
	if math.Abs(stop-85999.98) > 1e-9 {
		t.Fatalf("stop: got %v want 85999.98", stop)
	}
}

func TestCryptoHasNoPipConvention(t *testing.T) {
	for _, symbol := range []string{"BTCUSDT", "ETHUSDT", "XRPUSDT"} {
		spec, _ := Lookup(symbol)
		if spec.HasPipConvention() {
			t.Fatalf("%s must not claim a pip convention", symbol)
		}
	}
	fx, _ := Lookup("EURUSD")
	if !fx.HasPipConvention() {
		t.Fatal("EURUSD must have a pip convention")
	}
}

// Feeds spell the same instrument differently; all spellings must resolve to
// one spec so the live engine and the research feed cannot disagree.
func TestNormalizeAcceptsEveryFeedSpelling(t *testing.T) {
	for _, spelling := range []string{"EURUSD", "EUR/USD", "eur-usd", " eur usd ", "EUR_USD"} {
		if got := Normalize(spelling); got != "EURUSD" {
			t.Fatalf("Normalize(%q) = %q", spelling, got)
		}
		spec, ok := Lookup(spelling)
		if !ok || spec.Symbol != "EURUSD" {
			t.Fatalf("Lookup(%q) failed to resolve", spelling)
		}
	}
	if got := Normalize("BTCUSDT"); got != "BTCUSDT" {
		t.Fatalf("Normalize(BTCUSDT) = %q", got)
	}
}

// Lookup must fail closed. A guessed scale is worse than a refusal because
// it silently changes what every tick parameter means.
func TestLookupFailsClosedOnUnknownInstruments(t *testing.T) {
	for _, symbol := range []string{"DOGEUSD", "SOMETHINGELSE", "", "ZZZZZZ"} {
		if _, ok := Lookup(symbol); ok {
			t.Fatalf("%q must not resolve", symbol)
		}
	}
}

// Instruments carried over from prior convention are flagged so they cannot
// be mistaken for venue-verified precision.
func TestCarriedOverInstrumentsAreMarkedUnverified(t *testing.T) {
	for _, symbol := range []string{"BRENT", "NQ", "US30"} {
		spec, ok := Lookup(symbol)
		if !ok {
			t.Fatalf("%s must remain resolvable for backwards compatibility", symbol)
		}
		if !spec.Unverified {
			t.Fatalf("%s precision is not venue-verified and must say so", symbol)
		}
	}
}

func TestTicksRoundsHalfAwayFromZero(t *testing.T) {
	eur, _ := Lookup("EURUSD")
	// 1.000005 sits exactly between two ticks and must not truncate down.
	if got := eur.Ticks(1.000005); got != 100001 {
		t.Fatalf("Ticks(1.000005) = %d want 100001", got)
	}
	// Truncation was the old behaviour and lost a tick on common prices.
	if got := eur.Ticks(1.08432); got != 108432 {
		t.Fatalf("Ticks(1.08432) = %d want 108432", got)
	}
	if got := eur.Ticks(1.10366); got != 110366 {
		t.Fatalf("Ticks(1.10366) = %d want 110366", got)
	}
}

func TestSymbolsIsStableAndComplete(t *testing.T) {
	symbols := Symbols()
	if len(symbols) != len(registry) {
		t.Fatalf("got %d symbols, registry holds %d", len(symbols), len(registry))
	}
	for i := 1; i < len(symbols); i++ {
		if symbols[i-1] >= symbols[i] {
			t.Fatal("Symbols must be sorted for stable output")
		}
	}
}
