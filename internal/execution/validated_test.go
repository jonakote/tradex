package execution

import "testing"

func TestIsValidated(t *testing.T) {
	cases := []struct {
		strategy, symbol string
		want             bool
	}{
		{"IBS", "US100", true},       // real, both-halves-confirmed entry
		{"ibs", "US100", true},       // case-insensitive
		{"IBS", "DE40", false},       // tested and excluded (sign flip)
		{"IBS", "NOTREAL", false},    // absent entirely
		{"RSI2", "US500", true},
		{"RSI2", "EURUSD", true},     // intraday RSI2 (15m, late-session) confirmed 2026-11-28
		{"RSI2", "GBPUSD", false},    // intraday RSI2 does not confirm on GBPUSD
		{"CONFLUENCE", "XAUUSD", true},
		{"CONFLUENCE", "USDCAD", false}, // confluence flips negative on USDCAD
		{"UNKNOWNSTRATEGY", "US100", false},
	}
	for _, c := range cases {
		if got := IsValidated(c.strategy, c.symbol); got != c.want {
			t.Errorf("IsValidated(%q, %q) = %v, want %v", c.strategy, c.symbol, got, c.want)
		}
	}
}

// TestIsValidatedNeverTrueForAbsentEntry guards the core safety property:
// absence from a Measured*Stats table must never be silently read as
// confirmation, regardless of which strategy name is passed.
func TestIsValidatedNeverTrueForAbsentEntry(t *testing.T) {
	if IsValidated("IBS", "SOME_SYMBOL_NEVER_MEASURED") {
		t.Fatal("IsValidated must be false for a symbol absent from the stats table")
	}
}
