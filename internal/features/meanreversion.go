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
