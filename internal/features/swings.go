package features

import (
	"errors"
	"time"

	"tradex/internal/domain"
)

type SwingKind string

const (
	SwingHigh SwingKind = "SWING_HIGH"
	SwingLow  SwingKind = "SWING_LOW"
)

type Swing struct {
	Kind        SwingKind
	Symbol      string
	Timeframe   time.Duration
	Price       domain.Price
	PivotTime   time.Time
	ConfirmedAt time.Time
	PivotIndex  int
}

type SwingDetector struct {
	left      int
	right     int
	buffer    []domain.Candle
	seen      int
	source    string
	symbol    string
	timeframe time.Duration
}

func NewSwingDetector(left, right int) (*SwingDetector, error) {
	if left <= 0 || right <= 0 {
		return nil, errors.New("swing left and right windows must be positive")
	}
	return &SwingDetector{left: left, right: right}, nil
}

func (d *SwingDetector) Push(candle domain.Candle) ([]Swing, error) {
	if err := candle.Validate(); err != nil {
		return nil, err
	}
	if d.seen == 0 {
		d.source, d.symbol, d.timeframe = candle.Source, candle.Symbol, candle.Timeframe
	} else {
		if candle.Source != d.source || candle.Symbol != d.symbol || candle.Timeframe != d.timeframe {
			return nil, errors.New("candle changes swing-detector stream")
		}
		previous := d.buffer[len(d.buffer)-1]
		if !candle.OpenTime.After(previous.OpenTime) {
			return nil, errors.New("candle is duplicate or out of order")
		}
	}

	d.buffer = append(d.buffer, candle)
	d.seen++
	window := d.left + d.right + 1
	if len(d.buffer) < window {
		return nil, nil
	}
	if len(d.buffer) > window {
		d.buffer = d.buffer[len(d.buffer)-window:]
	}

	center := d.buffer[d.left]
	isHigh, isLow := true, true
	for i, neighbor := range d.buffer {
		if i == d.left {
			continue
		}
		if center.High <= neighbor.High {
			isHigh = false
		}
		if center.Low >= neighbor.Low {
			isLow = false
		}
	}

	pivotIndex := d.seen - d.right - 1
	confirmedAt := candle.CloseTime
	result := make([]Swing, 0, 2)
	if isHigh {
		result = append(result, Swing{
			Kind: SwingHigh, Symbol: center.Symbol, Timeframe: center.Timeframe,
			Price: center.High, PivotTime: center.CloseTime, ConfirmedAt: confirmedAt,
			PivotIndex: pivotIndex,
		})
	}
	if isLow {
		result = append(result, Swing{
			Kind: SwingLow, Symbol: center.Symbol, Timeframe: center.Timeframe,
			Price: center.Low, PivotTime: center.CloseTime, ConfirmedAt: confirmedAt,
			PivotIndex: pivotIndex,
		})
	}
	return result, nil
}

type Trend string

const (
	TrendBullish Trend = "BULLISH"
	TrendBearish Trend = "BEARISH"
	TrendNeutral Trend = "NEUTRAL"
)

func ClassifyTrend(swings []Swing) Trend {
	var highs, lows []Swing
	for _, swing := range swings {
		switch swing.Kind {
		case SwingHigh:
			highs = append(highs, swing)
		case SwingLow:
			lows = append(lows, swing)
		}
	}
	if len(highs) < 2 || len(lows) < 2 {
		return TrendNeutral
	}
	previousHigh, currentHigh := highs[len(highs)-2], highs[len(highs)-1]
	previousLow, currentLow := lows[len(lows)-2], lows[len(lows)-1]
	if currentHigh.Price > previousHigh.Price && currentLow.Price > previousLow.Price {
		return TrendBullish
	}
	if currentHigh.Price < previousHigh.Price && currentLow.Price < previousLow.Price {
		return TrendBearish
	}
	return TrendNeutral
}
