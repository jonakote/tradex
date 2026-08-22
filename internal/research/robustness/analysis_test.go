package robustness

import (
	"math"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"tradex/internal/analytics"
	"tradex/internal/domain"
	"tradex/internal/portfolio"
	"tradex/internal/research/replay"
)

func TestRobustnessAnalysisIsSeededHashedAndNewOnly(t *testing.T) {
	result := syntheticReplayResult(t, []int64{100, -50, 200, -100, 150, -25})
	config := Config{
		Iterations: 2000, MeanBlockLength: 2, Seed: 42, RuinDrawdownBPS: 500,
		IndependentTrials: 3, TrialSharpeRatios: []float64{-0.2, 0.1, 0.4, 0.7},
	}
	analysis, err := FromSingle(result, config)
	if err != nil {
		t.Fatal(err)
	}
	second, err := FromSingle(result, config)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(analysis, second) {
		t.Fatal("same seed and input produced different robustness results")
	}
	if analysis.TradeCount != 6 || analysis.Observed.EndingPnLMinor != 275 ||
		analysis.Observed.MaxDrawdownMinor != 100 || analysis.Observed.MaxConsecutiveLosses != 1 {
		t.Fatalf("unexpected observed path: %+v", analysis.Observed)
	}
	if analysis.Bootstrap.Iterations != 2000 || analysis.Bootstrap.MaxDrawdownMinor.P95 < 0 ||
		!probability(analysis.Bootstrap.ProbabilityOfLoss) || !probability(analysis.Bootstrap.ProbabilityOfRuinThreshold) {
		t.Fatalf("unexpected bootstrap summary: %+v", analysis.Bootstrap)
	}
	if analysis.DeflatedSharpe.ReturnUnit != "PER_TRADE_EXCESS_RETURN" ||
		!probability(analysis.DeflatedSharpe.DeflatedSharpeProbability) ||
		analysis.DeflatedSharpe.ExpectedMaximumSharpe <= 0 {
		t.Fatalf("unexpected Deflated Sharpe result: %+v", analysis.DeflatedSharpe)
	}
	if err := analysis.Verify(); err != nil {
		t.Fatal(err)
	}
	differentSeed := config
	differentSeed.Seed++
	third, err := FromSingle(result, differentSeed)
	if err != nil {
		t.Fatal(err)
	}
	if third.ResultSHA256 == analysis.ResultSHA256 || reflect.DeepEqual(third.Bootstrap, analysis.Bootstrap) {
		t.Fatal("changing the seed did not change the simulation")
	}

	path := filepath.Join(t.TempDir(), "robustness.json")
	if err := WriteResultNew(path, analysis); err != nil {
		t.Fatal(err)
	}
	if err := WriteResultNew(path, analysis); err == nil {
		t.Fatal("robustness result overwrote an existing artifact")
	}
	tampered := analysis
	tampered.Bootstrap.MaxDrawdownMinor.P95++
	if err := tampered.Verify(); err == nil {
		t.Fatal("tampered robustness result passed verification")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}

func TestStationaryBootstrapWithUnitBlockIsIIDAndDeterministic(t *testing.T) {
	config := Config{
		Iterations: 1000, MeanBlockLength: 1, Seed: 7, RuinDrawdownBPS: 1000,
		IndependentTrials: 2, TrialSharpeRatios: []float64{0.1, 0.2},
	}
	first, err := stationaryBootstrap([]int64{-10, 20, 30, -5}, 1000, config)
	if err != nil {
		t.Fatal(err)
	}
	second, err := stationaryBootstrap([]int64{-10, 20, 30, -5}, 1000, config)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) || first.EndingPnLMinor.P05 > first.EndingPnLMinor.P95 {
		t.Fatalf("invalid deterministic IID bootstrap: %+v %+v", first, second)
	}
}

func TestNormalQuantileAndDSRInputs(t *testing.T) {
	if difference := math.Abs(inverseNormalCDF(0.975) - 1.959963984540054); difference > 1e-9 {
		t.Fatalf("normal inverse error %g", difference)
	}
	if difference := math.Abs(normalCDF(inverseNormalCDF(0.1234)) - 0.1234); difference > 1e-12 {
		t.Fatalf("normal round-trip error %g", difference)
	}
	dsr, err := calculateDeflatedSharpe(
		[]float64{0.01, -0.005, 0.02, 0.003, -0.002, 0.015},
		[]float64{-0.1, 0.2, 0.4, 0.8}, 3,
	)
	if err != nil {
		t.Fatal(err)
	}
	if dsr.SampleCount != 6 || dsr.TrialSharpeVariance <= 0 || !probability(dsr.DeflatedSharpeProbability) {
		t.Fatalf("invalid DSR: %+v", dsr)
	}
}

func TestConfigAndSampleSizeFailClosed(t *testing.T) {
	config := Config{Iterations: 999, MeanBlockLength: 1, RuinDrawdownBPS: 500,
		IndependentTrials: 1, TrialSharpeRatios: []float64{0, 1}}
	if err := config.Validate(); err == nil {
		t.Fatal("undersized simulation passed validation")
	}
	config.Iterations = 1000
	result := syntheticReplayResult(t, []int64{10, -5})
	if _, err := FromSingle(result, config); err == nil {
		t.Fatal("undersized return sample passed robustness analysis")
	}
}

func TestSharedRobustnessUsesCombinedAccountTradePath(t *testing.T) {
	base, err := replay.Load(filepath.Join("..", "replay", "testdata", "eurusd_icc_win.json"))
	if err != nil {
		t.Fatal(err)
	}
	eurusd := base
	gbpusd := base
	gbpusd.Name = "gbpusd-robustness"
	gbpusd.Instrument.Symbol = "GBPUSD"
	gbpusd.Instrument.BaseCurrency = "GBP"
	gbpusd.Events = cloneRobustnessEvents(base.Events, "GBPUSD")
	shared := replay.SharedScenario{
		Name: "shared-robustness", InitialEquityMinor: base.InitialEquityMinor, Risk: base.Risk,
		Legs: []replay.Scenario{eurusd, gbpusd},
	}
	manifest, err := replay.SharedManifestFor(shared)
	if err != nil {
		t.Fatal(err)
	}
	fills := []domain.Fill{
		{Symbol: "EURUSD", Role: domain.FillTakeProfit, ClosedTradeNetMinor: 100},
		{Symbol: "GBPUSD", Role: domain.FillStopLoss, ClosedTradeNetMinor: -50},
		{Symbol: "EURUSD", Role: domain.FillTakeProfit, ClosedTradeNetMinor: 150},
		{Symbol: "GBPUSD", Role: domain.FillTakeProfit, ClosedTradeNetMinor: 75},
	}
	accountReport, err := analytics.BuildReport(fills, base.InitialEquityMinor)
	if err != nil {
		t.Fatal(err)
	}
	legReports := make(map[string]analytics.Report)
	for _, symbol := range []string{"EURUSD", "GBPUSD"} {
		var selected []domain.Fill
		for _, fill := range fills {
			if fill.Symbol == symbol {
				selected = append(selected, fill)
			}
		}
		legReports[symbol], err = analytics.BuildReport(selected, base.InitialEquityMinor)
		if err != nil {
			t.Fatal(err)
		}
	}
	result := replay.SharedResult{
		Scenario: shared.Name, Manifest: manifest, Fills: fills, Report: accountReport, LegReports: legReports,
		Portfolio: portfolio.View{
			EquityMinor:         base.InitialEquityMinor + accountReport.NetProfitMinor,
			BucketOpenRiskMinor: map[string]int64{}, DirectionalRiskMinor: map[string]int64{},
		},
	}
	config := Config{
		Iterations: 1000, MeanBlockLength: 2, Seed: 123, RuinDrawdownBPS: 500,
		IndependentTrials: 2, TrialSharpeRatios: []float64{0.1, 0.3},
	}
	analysis, err := FromShared(result, config)
	if err != nil {
		t.Fatal(err)
	}
	if analysis.Mode != "shared" || analysis.TradeCount != 4 || analysis.Observed.EndingPnLMinor != 275 {
		t.Fatalf("unexpected shared robustness result: %+v", analysis)
	}
}

func syntheticReplayResult(t *testing.T, pnls []int64) replay.Result {
	t.Helper()
	scenario, err := replay.Load(filepath.Join("..", "replay", "testdata", "eurusd_icc_win.json"))
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := replay.ManifestFor(scenario)
	if err != nil {
		t.Fatal(err)
	}
	fills := make([]domain.Fill, len(pnls))
	for index, pnl := range pnls {
		role := domain.FillTakeProfit
		if pnl < 0 {
			role = domain.FillStopLoss
		}
		fills[index] = domain.Fill{Symbol: scenario.Instrument.Symbol, Role: role, ClosedTradeNetMinor: pnl}
	}
	report, err := analytics.BuildReport(fills, scenario.InitialEquityMinor)
	if err != nil {
		t.Fatal(err)
	}
	return replay.Result{
		Scenario: scenario.Name, Manifest: manifest, Fills: fills, Report: report,
		Portfolio: portfolio.View{
			EquityMinor:         scenario.InitialEquityMinor + report.NetProfitMinor,
			BucketOpenRiskMinor: map[string]int64{}, DirectionalRiskMinor: map[string]int64{},
		},
	}
}

func cloneRobustnessEvents(source []replay.Event, symbol string) []replay.Event {
	result := make([]replay.Event, len(source))
	for index, event := range source {
		result[index] = event
		if event.Candle != nil {
			candle := *event.Candle
			candle.Symbol = symbol
			result[index].Candle = &candle
		}
		if event.Quote != nil {
			quote := *event.Quote
			quote.Symbol = symbol
			result[index].Quote = &quote
		}
	}
	return result
}
