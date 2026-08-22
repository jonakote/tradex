package marketdata

import (
	"fmt"
	"time"

	"tradex/internal/domain"
)

type Gap struct {
	After          time.Time
	Before         time.Time
	MissingPeriods int
}

type ValidationReport struct {
	Candles int
	Gaps    []Gap
}

func ValidateCandles(candles []domain.Candle) (ValidationReport, error) {
	report := ValidationReport{Candles: len(candles)}
	for i, candle := range candles {
		if err := candle.Validate(); err != nil {
			return ValidationReport{}, fmt.Errorf("candle %d: %w", i, err)
		}
		if i == 0 {
			continue
		}
		previous := candles[i-1]
		if candle.Source != previous.Source || candle.Symbol != previous.Symbol || candle.Timeframe != previous.Timeframe {
			return ValidationReport{}, fmt.Errorf("candle %d changes stream identity", i)
		}
		if !candle.OpenTime.After(previous.OpenTime) {
			return ValidationReport{}, fmt.Errorf("candle %d is duplicate or out of order", i)
		}
		if candle.OpenTime.Before(previous.CloseTime) {
			return ValidationReport{}, fmt.Errorf("candle %d overlaps prior candle", i)
		}
		if candle.OpenTime.After(previous.CloseTime) {
			delta := candle.OpenTime.Sub(previous.CloseTime)
			if delta%candle.Timeframe != 0 {
				return ValidationReport{}, fmt.Errorf("candle %d is not timeframe-aligned", i)
			}
			report.Gaps = append(report.Gaps, Gap{
				After: previous.CloseTime, Before: candle.OpenTime,
				MissingPeriods: int(delta / candle.Timeframe),
			})
		}
	}
	return report, nil
}
