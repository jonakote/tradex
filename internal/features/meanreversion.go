// Mean-reversion signals. Unlike the breakout/continuation detectors these
// bet that an extreme reverses rather than continues, which is the structure
// that produces a high win rate (small frequent gains, rare large losses).
//
// A high win rate is NOT an edge: mean reversion's per-trade profit is small,
// so realistic cost takes a larger share than it does from a trend trade.
// These are measured in internal/research/mrlab before anything is believed.
package features

import "tradex/internal/domain"

// MeanReversionSpecVersion is stamped on measured results.
const MeanReversionSpecVersion = "meanrev-0.1.0"

// IBSBps returns Internal Bar Strength in basis points: where the close sits
// inside the bar's own range, 0 at the low, 10000 at the high. ok is false
// for a zero-range bar, where IBS is undefined rather than zero.
//
// Pure integer arithmetic on one bar, no window and no state, so it is
// bit-reproducible. The documented effect (Pagonidis 2013): low IBS closes
// tend to bounce, high IBS closes tend to fade — but only measured on US
// equity ETFs, which is exactly why it must be re-measured on this universe.
func IBSBps(c domain.Candle) (int64, bool) {
	rng := int64(c.High - c.Low)
	if rng <= 0 {
		return 0, false
	}
	return int64(c.Close-c.Low) * 10000 / rng, true
}

// RSI2Bps returns Connors' 2-period RSI in basis points (0 = maximally
// oversold, 10000 = maximally overbought), from a window's last 3 closes
// (2 bar-over-bar moves). ok is false when fewer than 3 closes are
// available or both moves are flat (RS undefined).
//
// Pure integer-scaled arithmetic over a fixed 3-candle window, no state
// carried between calls, so it is bit-reproducible like IBSBps. The
// documented effect (Connors, "Short Term Trading Strategies That Work"):
// RSI(2) below ~10 tends to bounce, above ~90 tends to fade — originally
// measured on US equities, re-measured on this project's own universe
// before being trusted (see docs/MEANREVERSION_SPEC.md).
func RSI2Bps(window []domain.Candle) (int64, bool) {
	n := len(window)
	if n < 3 {
		return 0, false
	}
	c0, c1, c2 := window[n-3].Close, window[n-2].Close, window[n-1].Close
	var gain, loss int64
	for _, diff := range []int64{int64(c1 - c0), int64(c2 - c1)} {
		if diff > 0 {
			gain += diff
		} else {
			loss += -diff
		}
	}
	if gain == 0 && loss == 0 {
		return 0, false
	}
	// RSI = 100 * avgGain / (avgGain + avgLoss); the /2 averaging cancels.
	return gain * 10000 / (gain + loss), true
}
