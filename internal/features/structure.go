package features

import (
	"time"

	"tradex/internal/domain"
)

type BreakDirection string

const (
	BreakBullish BreakDirection = "BULLISH_BREAK"
	BreakBearish BreakDirection = "BEARISH_BREAK"
)

type StructureBreak struct {
	Direction   BreakDirection
	BrokenSwing Swing
	Candle      domain.Candle
	KnownAt     time.Time
}

type StructureDetector struct {
	lastHigh       *Swing
	lastLow        *Swing
	brokenHighTime time.Time
	brokenLowTime  time.Time
}

func (d *StructureDetector) OnCandle(candle domain.Candle, confirmed []Swing) []StructureBreak {
	for i := range confirmed {
		swing := confirmed[i]
		if swing.Kind == SwingHigh {
			copy := swing
			d.lastHigh = &copy
		} else if swing.Kind == SwingLow {
			copy := swing
			d.lastLow = &copy
		}
	}

	breaks := make([]StructureBreak, 0, 1)
	if d.lastHigh != nil && candle.Close > d.lastHigh.Price && d.brokenHighTime != d.lastHigh.PivotTime {
		breaks = append(breaks, StructureBreak{
			Direction: BreakBullish, BrokenSwing: *d.lastHigh, Candle: candle, KnownAt: candle.CloseTime,
		})
		d.brokenHighTime = d.lastHigh.PivotTime
	}
	if d.lastLow != nil && candle.Close < d.lastLow.Price && d.brokenLowTime != d.lastLow.PivotTime {
		breaks = append(breaks, StructureBreak{
			Direction: BreakBearish, BrokenSwing: *d.lastLow, Candle: candle, KnownAt: candle.CloseTime,
		})
		d.brokenLowTime = d.lastLow.PivotTime
	}
	return breaks
}
