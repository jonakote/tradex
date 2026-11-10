package features

import "tradex/internal/domain"

// HTFTrendConfig controls the windowed, higher-timeframe trend read used to
// gate a lower-timeframe entry (see internal/strategy/scan/htf.go). It
// reuses the project's existing swing-structure trend classifier
// (NewSwingDetector + ClassifyTrend, the same mechanism StructureTrend
// already measures) rather than introducing a second, untested notion of
// "trend" — consistent with this project's rule against stacking unmeasured
// mechanisms.
type HTFTrendConfig struct {
	// SwingLeft/SwingRight size the pivot window, same meaning as
	// StructureTrendConfig's fields of the same name.
	SwingLeft  int `json:"swing_left"`
	SwingRight int `json:"swing_right"`
}

func DefaultHTFTrendConfig() HTFTrendConfig {
	return HTFTrendConfig{SwingLeft: 3, SwingRight: 2}
}

func (c HTFTrendConfig) Validate() error {
	if c.SwingLeft <= 0 || c.SwingRight <= 0 {
		return errTrendSwingWindow
	}
	return nil
}

var errTrendSwingWindow = trendError("htf trend swing left/right windows must be positive")

type trendError string

func (e trendError) Error() string { return string(e) }

// RequiredBars is a stated prior, mirroring StructureTrendConfig.RequiredBars:
// enough pivot windows for ClassifyTrend to plausibly confirm 2+ swing
// highs and 2+ swing lows.
func (c HTFTrendConfig) RequiredBars() int {
	pivot := c.SwingLeft + c.SwingRight + 1
	required := pivot * 10
	if required < 60 {
		required = 60
	}
	return required
}

// DetectHTFTrend recomputes swings fresh over the whole window (oldest to
// newest closed bars) and classifies the trend as of the last bar. Returns
// (TrendNeutral, false) on insufficient history; a window long enough but
// genuinely directionless returns (TrendNeutral, true) — callers must tell
// those apart via the bool, never treat "not enough data" as "range-bound".
func DetectHTFTrend(window []domain.Candle, config HTFTrendConfig) (Trend, bool) {
	if err := config.Validate(); err != nil {
		return TrendNeutral, false
	}
	if len(window) < config.RequiredBars() {
		return TrendNeutral, false
	}
	swingDet, err := NewSwingDetector(config.SwingLeft, config.SwingRight)
	if err != nil {
		return TrendNeutral, false
	}
	var allSwings []Swing
	for _, candle := range window {
		newSwings, err := swingDet.Push(candle)
		if err != nil {
			return TrendNeutral, false
		}
		allSwings = append(allSwings, newSwings...)
	}
	return ClassifyTrend(allSwings), true
}
