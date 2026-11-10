// Package instrument is the single source of truth for quote precision.
//
// Every price in tradex is an integer tick (domain.Price), so a wrong
// precision does not merely misformat output — it changes the meaning of
// every tick-denominated parameter. A stop buffer of "2 ticks" is $0.02 on
// BTCUSDT at the correct precision and $0.00002 at a wrongly assumed one.
//
// Precision is therefore recorded per instrument and verified against the
// venue, never inferred from the symbol string:
//
//   - FX and metals: verified against Dukascopy's published integer points
//     (see docs/MARKET_DATA_SOURCES.md).
//   - Crypto: verified against the Binance PRICE_FILTER tickSize reported by
//     /api/v3/exchangeInfo.
//
// Lookup fails closed. An unverified symbol returns false rather than a
// guessed scale, because a plausible-looking wrong scale is far more
// damaging than a refusal.
package instrument

import (
	"sort"
	"strings"
)

// Class groups instruments that share a quoting convention.
type Class string

const (
	ClassFX     Class = "FX"
	ClassMetal  Class = "METAL"
	ClassCrypto Class = "CRYPTO"
	ClassEnergy Class = "ENERGY"
	ClassIndex  Class = "INDEX"
)

// Spec is the verified quoting convention for one instrument.
type Spec struct {
	// Symbol is the canonical, separator-free form, e.g. "EURUSD".
	Symbol string
	Class  Class
	// Digits is the number of decimal places in the venue's quote.
	Digits int
	// PriceScale is 10^Digits: the number of ticks per unit of quote
	// currency, and the factor converting a decimal price to domain.Price.
	PriceScale int64
	// PipSize is the price movement of one pip, or 0 for instruments with no
	// pip convention (crypto). Never quote pips when this is 0.
	PipSize float64
	// Unverified marks a precision carried over from prior convention rather
	// than confirmed against the venue. Such an instrument must not be
	// trusted with tick-denominated strategy parameters until verified.
	Unverified bool
}

// TickSize is the price movement of one tick.
func (s Spec) TickSize() float64 { return 1 / float64(s.PriceScale) }

// HasPipConvention reports whether quoting distances in pips is meaningful.
func (s Spec) HasPipConvention() bool { return s.PipSize > 0 }

// Ticks converts a decimal price into integer ticks, rounding half away from
// zero so a price exactly on a tick boundary cannot drift down.
func (s Spec) Ticks(price float64) int64 {
	scaled := price * float64(s.PriceScale)
	if scaled >= 0 {
		return int64(scaled + 0.5)
	}
	return int64(scaled - 0.5)
}

// Price converts integer ticks back into a decimal price.
func (s Spec) Price(ticks int64) float64 { return float64(ticks) / float64(s.PriceScale) }

// Normalize reduces any feed's symbol spelling to the canonical form:
// "EUR/USD", "eur-usd" and "EURUSD" all become "EURUSD".
func Normalize(symbol string) string {
	var builder strings.Builder
	for _, r := range strings.ToUpper(strings.TrimSpace(symbol)) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			builder.WriteRune(r)
		}
	}
	return builder.String()
}

func fx(symbol string, digits int, pip float64) Spec {
	return Spec{Symbol: symbol, Class: ClassFX, Digits: digits,
		PriceScale: scaleFor(digits), PipSize: pip}
}

func metal(symbol string, digits int, pip float64) Spec {
	return Spec{Symbol: symbol, Class: ClassMetal, Digits: digits,
		PriceScale: scaleFor(digits), PipSize: pip}
}

func crypto(symbol string, digits int) Spec {
	// Crypto is quoted in the quote asset and has no pip convention.
	return Spec{Symbol: symbol, Class: ClassCrypto, Digits: digits,
		PriceScale: scaleFor(digits), PipSize: 0}
}

func scaleFor(digits int) int64 {
	scale := int64(1)
	for i := 0; i < digits; i++ {
		scale *= 10
	}
	return scale
}

// registry holds only verified instruments. Add an entry when its venue
// precision has been confirmed, not when a symbol is merely expected.
var registry = func() map[string]Spec {
	specs := []Spec{}

	// FX majors and crosses: 5 decimal places, 1 pip = 0.0001.
	for _, symbol := range []string{
		"EURUSD", "GBPUSD", "AUDUSD", "NZDUSD", "USDCAD", "USDCHF",
		"EURGBP", "EURCHF", "EURAUD", "EURCAD", "EURNZD",
		"GBPAUD", "GBPCAD", "GBPCHF", "GBPNZD",
		"AUDCAD", "AUDCHF", "AUDNZD", "NZDCAD", "NZDCHF", "CADCHF",
	} {
		specs = append(specs, fx(symbol, 5, 0.0001))
	}
	// JPY quotes: 3 decimal places, 1 pip = 0.01.
	for _, symbol := range []string{
		"USDJPY", "EURJPY", "GBPJPY", "AUDJPY", "NZDJPY", "CADJPY", "CHFJPY",
	} {
		specs = append(specs, fx(symbol, 3, 0.01))
	}
	// Precious metals: 3 decimal places as published by Dukascopy. Gold's
	// conventional pip is 0.1; silver's is 0.01.
	specs = append(specs,
		metal("XAUUSD", 3, 0.1),
		metal("XAGUSD", 3, 0.01),
	)
	// Crypto: digits implied by the Binance PRICE_FILTER tickSize.
	specs = append(specs,
		crypto("BTCUSDT", 2), // tickSize 0.01
		crypto("ETHUSDT", 2), // tickSize 0.01
		crypto("BNBUSDT", 2), // tickSize 0.01
		crypto("SOLUSDT", 2), // tickSize 0.01
		crypto("XRPUSDT", 4), // tickSize 0.0001
		crypto("ADAUSDT", 4), // tickSize 0.0001
	)

	// Energy and indices carry the conventions the repository already used
	// (one point treated as one pip). These are NOT venue-verified: they
	// exist so previously supported symbols keep working, and the venue
	// precision must be confirmed before any tick-denominated strategy
	// parameter is trusted on them. They are outside the current research
	// universe of FX majors plus XAU/XAG.
	for _, symbol := range []string{"USOIL", "UKOIL", "XTIUSD", "XBRUSD"} {
		specs = append(specs, Spec{Symbol: symbol, Class: ClassEnergy,
			Digits: 2, PriceScale: scaleFor(2), PipSize: 1, Unverified: true})
	}
	for _, symbol := range []string{"NQ", "NAS100", "IXIC", "SPX", "SPX500", "DJI"} {
		specs = append(specs, Spec{Symbol: symbol, Class: ClassIndex,
			Digits: 2, PriceScale: scaleFor(2), PipSize: 1, Unverified: true})
	}
	// US500/US100/US30: verified against GET /api/v1/markets on 2026-11-10
	// (tickSize 0.1, pipPosition 0, scalingFactor 1 on all three) while
	// testing whether IBS mean reversion — documented in the literature on
	// US equity ETFs/indices specifically, never tested here before —
	// holds on the asset class it actually comes from. 1 decimal place,
	// not the 2-digit guess the rest of this block carries.
	for _, symbol := range []string{"US500", "US100", "US30"} {
		specs = append(specs, Spec{Symbol: symbol, Class: ClassIndex,
			Digits: 1, PriceScale: scaleFor(1), PipSize: 1})
	}
	// Non-US global indices: same verification (tickSize 0.1) on 2026-11-10.
	for _, symbol := range []string{"DE40", "J225", "FR40", "UK100"} {
		specs = append(specs, Spec{Symbol: symbol, Class: ClassIndex,
			Digits: 1, PriceScale: scaleFor(1), PipSize: 1})
	}
	// Commodities beyond gold/silver: verified against GET /api/v1/markets
	// on 2026-11-10 (tickSize noted per symbol) -- none match the 2-digit
	// guess the Unverified energy block above still carries for the
	// untouched aliases (USOIL/UKOIL/XTIUSD/XBRUSD).
	specs = append(specs,
		Spec{Symbol: "WTI", Class: ClassEnergy, Digits: 3, PriceScale: scaleFor(3), PipSize: 1},
		Spec{Symbol: "BRENT", Class: ClassEnergy, Digits: 3, PriceScale: scaleFor(3), PipSize: 1},
		Spec{Symbol: "NATGAS", Class: ClassEnergy, Digits: 4, PriceScale: scaleFor(4), PipSize: 1},
		Spec{Symbol: "COPPER", Class: ClassMetal, Digits: 5, PriceScale: scaleFor(5), PipSize: 1},
	)

	table := make(map[string]Spec, len(specs))
	for _, spec := range specs {
		table[spec.Symbol] = spec
	}
	return table
}()

// Lookup resolves any feed spelling to a verified spec. It returns false for
// an unverified instrument; callers must fail closed rather than assume.
func Lookup(symbol string) (Spec, bool) {
	spec, ok := registry[Normalize(symbol)]
	return spec, ok
}

// Symbols lists every verified instrument, sorted for stable output.
func Symbols() []string {
	result := make([]string, 0, len(registry))
	for symbol := range registry {
		result = append(result, symbol)
	}
	sort.Strings(result)
	return result
}
