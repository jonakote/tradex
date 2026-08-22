package analytics

import (
	"errors"
	"math"

	"tradex/internal/domain"
)

type Report struct {
	Trades               int
	Wins                 int
	Losses               int
	Breakeven            int
	WinRate              float64
	WinRateLower95       float64
	WinRateUpper95       float64
	NetProfitMinor       int64
	WinningPnLMinor      int64
	LosingPnLMinor       int64
	TotalCommissionMinor int64
	TotalRolloverMinor   int64
	ExpectancyMinor      float64
	ProfitFactor         float64
	ProfitFactorInfinite bool
	MaxDrawdownMinor     int64
	MaxConsecutiveLosses int
}

func BuildReport(fills []domain.Fill, initialEquityMinor int64) (Report, error) {
	if initialEquityMinor <= 0 {
		return Report{}, errors.New("initial equity must be positive")
	}
	report := Report{}
	equity, peak := initialEquityMinor, initialEquityMinor
	consecutiveLosses := 0
	for _, fill := range fills {
		if fill.CommissionMinor < 0 {
			return Report{}, errors.New("fill commission cannot be negative")
		}
		report.TotalCommissionMinor += fill.CommissionMinor
		report.TotalRolloverMinor += fill.RolloverMinor
		if fill.Role == domain.FillEntry {
			continue
		}
		if fill.Role != domain.FillStopLoss && fill.Role != domain.FillTakeProfit {
			return Report{}, errors.New("unknown closing fill role")
		}
		report.Trades++
		pnl := fill.ClosedTradeNetMinor
		report.NetProfitMinor += pnl
		equity += pnl
		if equity > peak {
			peak = equity
		}
		drawdown := peak - equity
		if drawdown > report.MaxDrawdownMinor {
			report.MaxDrawdownMinor = drawdown
		}
		switch {
		case pnl > 0:
			report.Wins++
			report.WinningPnLMinor += pnl
			consecutiveLosses = 0
		case pnl < 0:
			report.Losses++
			report.LosingPnLMinor += pnl
			consecutiveLosses++
			if consecutiveLosses > report.MaxConsecutiveLosses {
				report.MaxConsecutiveLosses = consecutiveLosses
			}
		default:
			report.Breakeven++
			consecutiveLosses = 0
		}
	}
	if report.Trades == 0 {
		return report, nil
	}
	report.WinRate = float64(report.Wins) / float64(report.Trades)
	report.WinRateLower95, report.WinRateUpper95 = wilson(report.Wins, report.Trades, 1.959963984540054)
	report.ExpectancyMinor = float64(report.NetProfitMinor) / float64(report.Trades)
	if report.LosingPnLMinor < 0 {
		report.ProfitFactor = float64(report.WinningPnLMinor) / float64(-report.LosingPnLMinor)
	} else if report.WinningPnLMinor > 0 {
		report.ProfitFactorInfinite = true
	}
	return report, nil
}

func wilson(successes, total int, z float64) (float64, float64) {
	if total == 0 {
		return 0, 0
	}
	n := float64(total)
	p := float64(successes) / n
	z2 := z * z
	denominator := 1 + z2/n
	center := (p + z2/(2*n)) / denominator
	margin := z * math.Sqrt((p*(1-p)+z2/(4*n))/n) / denominator
	return math.Max(0, center-margin), math.Min(1, center+margin)
}
