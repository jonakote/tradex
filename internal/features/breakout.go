// Trend-following breakout signal. Unlike mean-reversion (meanreversion.go)
// this bets that a new extreme continues rather than reverses -- the
// opposite structural bet, deliberately: a low win rate with winners that
// run far longer than losers, the "legitimate" way to get a good payoff
// ratio (see docs/STRATEGY_SUITE.md breadth round 7 for the real numbers
// and why this is structurally different from a dangerous high-win-rate/
// bad-payoff system).
package features

import "tradex/internal/domain"

// BreakoutSpecVersion is stamped on measured results.
const BreakoutSpecVersion = "breakout-0.1.0"

// DonchianBreakoutLong reports whether the last candle's close broke above
// the highest high of the preceding entryWindow candles (NOT including the
// current one) -- a classic turtle-system entry. Long only: measured
// evidence (breadth round 7, 2026-11-20) only confirmed the long side on
// BTCUSD; short was never tested and should not be assumed symmetric for a
// trend-following signal, unlike mean-reversion's IBS/RSI2.
func DonchianBreakoutLong(window []domain.Candle, entryWindow int) bool {
	if len(window) < entryWindow+1 {
		return false
	}
	last := window[len(window)-1]
	prior := window[len(window)-1-entryWindow : len(window)-1]
	var highest domain.Price
	for _, c := range prior {
		if c.High > highest {
			highest = c.High
		}
	}
	return last.Close > highest
}

// DonchianExitLong reports whether the last candle's close broke below the
// lowest low of the preceding exitWindow candles (not including the
// current one) -- the trailing-channel exit half of the same system.
// Recomputed fresh every scan from current data, unlike IBS/RSI2's
// single-bar oscillator check, because the channel itself moves every day.
func DonchianExitLong(window []domain.Candle, exitWindow int) bool {
	if len(window) < exitWindow+1 {
		return false
	}
	last := window[len(window)-1]
	prior := window[len(window)-1-exitWindow : len(window)-1]
	lowest := prior[0].Low
	for _, c := range prior {
		if c.Low < lowest {
			lowest = c.Low
		}
	}
	return last.Close < lowest
}
