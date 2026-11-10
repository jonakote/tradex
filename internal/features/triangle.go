package features

import (
	"errors"
	"math"
	"time"

	"tradex/internal/domain"
)

// TriangleKind is a structural consolidation hypothesis. A detected triangle
// is evidence only; it is not an executable trade by itself.
type TriangleKind string

const (
	TriangleAscending  TriangleKind = "ASCENDING_TRIANGLE"
	TriangleDescending TriangleKind = "DESCENDING_TRIANGLE"
	TriangleSymmetric  TriangleKind = "SYMMETRICAL_TRIANGLE"
)

// TriangleConfig controls the deterministic triangle detector. Price values
// are expressed in domain ticks, and slopes are ticks per pivot-index step.
type TriangleConfig struct {
	MinTouches            int   `json:"min_touches"`
	FlatSlopeTicks        int64 `json:"flat_slope_ticks"`
	MinSlopeTicksPerPivot int64 `json:"min_slope_ticks_per_pivot"`
	ToleranceTicks        int64 `json:"tolerance_ticks"`
	MinHeightTicks        int64 `json:"min_height_ticks"`
}

func DefaultTriangleConfig() TriangleConfig {
	return TriangleConfig{
		MinTouches:            3,
		FlatSlopeTicks:        2,
		MinSlopeTicksPerPivot: 5,
		ToleranceTicks:        5,
		MinHeightTicks:        25,
	}
}

func (c TriangleConfig) Validate() error {
	if c.MinTouches < 3 {
		return errors.New("triangle minimum touches must be at least three")
	}
	if c.FlatSlopeTicks < 0 || c.MinSlopeTicksPerPivot <= 0 || c.ToleranceTicks < 0 || c.MinHeightTicks <= 0 {
		return errors.New("triangle thresholds must be non-negative and slopes/height must be positive")
	}
	if c.MinSlopeTicksPerPivot <= c.FlatSlopeTicks {
		return errors.New("triangle minimum slope must exceed flat slope")
	}
	return nil
}

// TrianglePattern is a confirmed, timestamped geometric candidate built only
// from confirmed swings. Upper/lower lines are represented by their endpoint
// pivots and can be projected without re-reading future candles.
type TrianglePattern struct {
	Kind            TriangleKind `json:"kind"`
	UpperStartIndex int          `json:"upper_start_index"`
	UpperEndIndex   int          `json:"upper_end_index"`
	UpperStartPrice domain.Price `json:"upper_start_price"`
	UpperEndPrice   domain.Price `json:"upper_end_price"`
	LowerStartIndex int          `json:"lower_start_index"`
	LowerEndIndex   int          `json:"lower_end_index"`
	LowerStartPrice domain.Price `json:"lower_start_price"`
	LowerEndPrice   domain.Price `json:"lower_end_price"`
	StartAt         time.Time    `json:"start_at"`
	DetectedAt      time.Time    `json:"detected_at"`
	LastPivotIndex  int          `json:"last_pivot_index"`
	ApexPivotIndex  float64      `json:"apex_pivot_index"`
	ToleranceTicks  int64        `json:"tolerance_ticks"`
}

func (p TrianglePattern) upperAt(index int) float64 {
	return lineAt(p.UpperStartIndex, p.UpperEndIndex, p.UpperStartPrice, p.UpperEndPrice, index)
}

func (p TrianglePattern) lowerAt(index int) float64 {
	return lineAt(p.LowerStartIndex, p.LowerEndIndex, p.LowerStartPrice, p.LowerEndPrice, index)
}

// UpperBoundaryAt and LowerBoundaryAt expose the projected boundaries for
// strategy and chart consumers without exposing mutable detector state.
func (p TrianglePattern) UpperBoundaryAt(index int) domain.Price {
	return domain.Price(math.Round(p.upperAt(index)))
}

func (p TrianglePattern) LowerBoundaryAt(index int) domain.Price {
	return domain.Price(math.Round(p.lowerAt(index)))
}

// Breakout returns a direction only after a completed candle closes beyond a
// projected boundary. A wick through a boundary is deliberately insufficient.
func (p TrianglePattern) Breakout(close domain.Price, index int, buffer domain.Price) (BreakDirection, bool) {
	if index <= p.LastPivotIndex {
		return "", false
	}
	upper := p.UpperBoundaryAt(index) + buffer
	lower := p.LowerBoundaryAt(index) - buffer
	if close > upper {
		return BreakBullish, true
	}
	if close < lower {
		return BreakBearish, true
	}
	return "", false
}

// DetectTriangle finds a triangle from the most recent confirmed highs and
// lows. It requires at least three touches on each boundary, bounded fitting
// error, positive height, and a future projected apex. It returns false for
// ambiguous geometry rather than inventing a pattern classification.
func DetectTriangle(swings []Swing, config TriangleConfig) (TrianglePattern, bool) {
	if config.Validate() != nil {
		return TrianglePattern{}, false
	}
	highs := confirmedSwings(swings, SwingHigh)
	lows := confirmedSwings(swings, SwingLow)
	if len(highs) < config.MinTouches || len(lows) < config.MinTouches {
		return TrianglePattern{}, false
	}
	highs = highs[len(highs)-config.MinTouches:]
	lows = lows[len(lows)-config.MinTouches:]
	if !sameStream(highs) || !sameStream(lows) {
		return TrianglePattern{}, false
	}
	upperSlope := slope(highs[0], highs[len(highs)-1])
	lowerSlope := slope(lows[0], lows[len(lows)-1])
	if highs[0].PivotIndex >= highs[len(highs)-1].PivotIndex || lows[0].PivotIndex >= lows[len(lows)-1].PivotIndex {
		return TrianglePattern{}, false
	}
	if !fitsLine(highs, config.ToleranceTicks) || !fitsLine(lows, config.ToleranceTicks) {
		return TrianglePattern{}, false
	}
	startGap := float64(highs[0].Price - lows[0].Price)
	endGap := float64(highs[len(highs)-1].Price - lows[len(lows)-1].Price)
	if startGap < float64(config.MinHeightTicks) || endGap <= 0 || endGap >= startGap-float64(config.ToleranceTicks) {
		return TrianglePattern{}, false
	}

	kind := TriangleKind("")
	flat := float64(config.FlatSlopeTicks)
	minSlope := float64(config.MinSlopeTicksPerPivot)
	switch {
	case math.Abs(upperSlope) <= flat && lowerSlope >= minSlope:
		kind = TriangleAscending
	case upperSlope <= -minSlope && math.Abs(lowerSlope) <= flat:
		kind = TriangleDescending
	case upperSlope <= -minSlope && lowerSlope >= minSlope:
		kind = TriangleSymmetric
	default:
		return TrianglePattern{}, false
	}

	lastPivot := highs[len(highs)-1].PivotIndex
	if lows[len(lows)-1].PivotIndex > lastPivot {
		lastPivot = lows[len(lows)-1].PivotIndex
	}
	denominator := upperSlope - lowerSlope
	if denominator >= 0 {
		return TrianglePattern{}, false
	}
	upperIntercept := float64(highs[0].Price) - upperSlope*float64(highs[0].PivotIndex)
	lowerIntercept := float64(lows[0].Price) - lowerSlope*float64(lows[0].PivotIndex)
	apex := (lowerIntercept - upperIntercept) / denominator
	if apex <= float64(lastPivot) {
		return TrianglePattern{}, false
	}

	startAt := highs[0].PivotTime
	if lows[0].PivotTime.Before(startAt) {
		startAt = lows[0].PivotTime
	}
	detectedAt := highs[len(highs)-1].ConfirmedAt
	if lows[len(lows)-1].ConfirmedAt.After(detectedAt) {
		detectedAt = lows[len(lows)-1].ConfirmedAt
	}
	return TrianglePattern{
		Kind:            kind,
		UpperStartIndex: highs[0].PivotIndex,
		UpperEndIndex:   highs[len(highs)-1].PivotIndex,
		UpperStartPrice: highs[0].Price,
		UpperEndPrice:   highs[len(highs)-1].Price,
		LowerStartIndex: lows[0].PivotIndex,
		LowerEndIndex:   lows[len(lows)-1].PivotIndex,
		LowerStartPrice: lows[0].Price,
		LowerEndPrice:   lows[len(lows)-1].Price,
		StartAt:         startAt.UTC(),
		DetectedAt:      detectedAt.UTC(),
		LastPivotIndex:  lastPivot,
		ApexPivotIndex:  apex,
		ToleranceTicks:  config.ToleranceTicks,
	}, true
}

func confirmedSwings(swings []Swing, kind SwingKind) []Swing {
	result := make([]Swing, 0, len(swings))
	for _, swing := range swings {
		if swing.Kind == kind && !swing.ConfirmedAt.IsZero() && swing.PivotIndex >= 0 {
			result = append(result, swing)
		}
	}
	return result
}

func sameStream(swings []Swing) bool {
	if len(swings) == 0 {
		return false
	}
	for i := 1; i < len(swings); i++ {
		if swings[i].Symbol != swings[0].Symbol || swings[i].Timeframe != swings[0].Timeframe ||
			!swings[i].PivotTime.After(swings[i-1].PivotTime) || swings[i].ConfirmedAt.Before(swings[i].PivotTime) {
			return false
		}
	}
	return swings[0].ConfirmedAt.After(swings[0].PivotTime) || swings[0].ConfirmedAt.Equal(swings[0].PivotTime)
}

func slope(first, last Swing) float64 {
	return float64(last.Price-first.Price) / float64(last.PivotIndex-first.PivotIndex)
}

func lineAt(startIndex, endIndex int, startPrice, endPrice domain.Price, index int) float64 {
	if endIndex == startIndex {
		return float64(startPrice)
	}
	return float64(startPrice) + float64(index-startIndex)*float64(endPrice-startPrice)/float64(endIndex-startIndex)
}

func fitsLine(swings []Swing, tolerance int64) bool {
	first, last := swings[0], swings[len(swings)-1]
	for _, swing := range swings {
		expected := lineAt(first.PivotIndex, last.PivotIndex, first.Price, last.Price, swing.PivotIndex)
		if math.Abs(float64(swing.Price)-expected) > float64(tolerance) {
			return false
		}
	}
	return true
}
