package analytics

import (
	"testing"

	"tradex/internal/domain"
)

func TestBuildReportIncludesConfidenceCostsAndDrawdown(t *testing.T) {
	fills := []domain.Fill{
		{Role: domain.FillEntry, CommissionMinor: 2},
		{Role: domain.FillTakeProfit, CommissionMinor: 2, RolloverMinor: -3, ClosedTradeNetMinor: 100},
		{Role: domain.FillEntry, CommissionMinor: 2},
		{Role: domain.FillStopLoss, CommissionMinor: 2, ClosedTradeNetMinor: -50},
		{Role: domain.FillEntry, CommissionMinor: 2},
		{Role: domain.FillTakeProfit, CommissionMinor: 2, ClosedTradeNetMinor: 0},
		{Role: domain.FillEntry, CommissionMinor: 2},
		{Role: domain.FillTakeProfit, CommissionMinor: 2, ClosedTradeNetMinor: 200},
	}
	report, err := BuildReport(fills, 100000)
	if err != nil {
		t.Fatal(err)
	}
	if report.Trades != 4 || report.Wins != 2 || report.Losses != 1 || report.Breakeven != 1 {
		t.Fatalf("unexpected counts: %+v", report)
	}
	if report.NetProfitMinor != 250 || report.MaxDrawdownMinor != 50 || report.TotalCommissionMinor != 16 {
		t.Fatalf("unexpected money metrics: %+v", report)
	}
	if report.TotalRolloverMinor != -3 {
		t.Fatalf("unexpected rollover metric: %+v", report)
	}
	if report.ExpectancyMinor != 62.5 || report.ProfitFactor != 6 || report.WinRate != 0.5 {
		t.Fatalf("unexpected ratios: %+v", report)
	}
	if report.WinRateLower95 <= 0 || report.WinRateUpper95 >= 1 || report.WinRateLower95 >= report.WinRateUpper95 {
		t.Fatalf("invalid Wilson interval: %+v", report)
	}
}

func TestBuildReportProfitFactorIsExplicitlyInfiniteWithoutLosses(t *testing.T) {
	report, err := BuildReport([]domain.Fill{{Role: domain.FillTakeProfit, ClosedTradeNetMinor: 10}}, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if report.ProfitFactor != 0 || !report.ProfitFactorInfinite {
		t.Fatalf("profit factor should use its JSON-safe infinity flag: %+v", report)
	}
}
