package crtlab

import (
	"encoding/json"
	"testing"
	"time"

	"tradex/internal/domain"
	"tradex/internal/strategy/crt"
)

var origin = time.Date(2026, time.August, 18, 0, 0, 0, 0, time.UTC)

func setupSeries(bars ...Bar) Series {
	return Series{Source: "test", Symbol: "EURUSD", Timeframe: 4 * time.Hour, Bars: bars}
}

func confirmSeries(bars ...Bar) Series {
	return Series{Source: "test", Symbol: "EURUSD", Timeframe: 15 * time.Minute, Bars: bars}
}

func setupBar(index int, open, high, low, close domain.Price) Bar {
	return Bar{
		OpenTime: origin.Add(time.Duration(index) * 4 * time.Hour),
		Open:     open, High: high, Low: low, Close: close, Ticks: 1,
	}
}

func confirmBar(index int, open, high, low, close domain.Price) Bar {
	return Bar{
		OpenTime: origin.Add(8 * time.Hour).Add(time.Duration(index) * 15 * time.Minute),
		Open:     open, High: high, Low: low, Close: close, Ticks: 1,
	}
}

// testParams uses the canonical bullish pattern: range 100000..100200, low
// purged to 99960, entry 100010, stop 99958, target 100200, risk 52 ticks.
func testParams() Params {
	params := DefaultParams()
	params.Costs = Costs{SpreadTicks: 2, SlippageTicks: 1} // 4 ticks per trade
	params.RiskMinorPerTrade = 10000
	return params
}

var (
	rangeBar = setupBar(0, 100050, 100200, 100000, 100150)
	purgeBar = setupBar(1, 100020, 100060, 99960, 100055)
)

func TestRunChargesCostsAndReportsAWinningTrade(t *testing.T) {
	setup := setupSeries(rangeBar, purgeBar, setupBar(2, 100100, 100210, 100000, 100180))
	confirm := confirmSeries(
		confirmBar(0, 100030, 100035, 100005, 100012), // trades to equilibrium
		confirmBar(1, 100050, 100210, 100045, 100200), // reaches the target
	)

	result, fills, err := Run(setup, confirm, testParams())
	if err != nil {
		t.Fatal(err)
	}
	if result.Funnel.Armed != 1 || result.Funnel.Filled != 1 || result.Funnel.TakeProfit != 1 {
		t.Fatalf("funnel: %+v", result.Funnel)
	}
	if result.Funnel.Expired != 0 || result.Funnel.StopLoss != 0 {
		t.Fatalf("unexpected funnel losses: %+v", result.Funnel)
	}
	if result.Report.Trades != 1 || result.Report.Wins != 1 {
		t.Fatalf("report: %+v", result.Report)
	}
	// Gross 190/52 = 3.6538R; costs are 4 ticks = 0.0769R.
	if result.Trades[0].GrossRBps != 36538 {
		t.Fatalf("gross R: got %d want 36538", result.Trades[0].GrossRBps)
	}
	if result.Trades[0].NetRBps != 36538-769 {
		t.Fatalf("net R: got %d want %d", result.Trades[0].NetRBps, 36538-769)
	}
	if result.ExpectancyRBps != result.Trades[0].NetRBps {
		t.Fatalf("expectancy must equal the only trade: %d", result.ExpectancyRBps)
	}
	if len(fills) != 2 || fills[0].Role != domain.FillEntry || fills[1].Role != domain.FillTakeProfit {
		t.Fatalf("fills: %+v", fills)
	}
	if result.SpecVersion == "" {
		t.Fatal("result must record the detector spec version")
	}
}

// A bar containing both levels must be charged as a loss, costs included.
func TestRunResolvesAmbiguousBarAsALossWithCosts(t *testing.T) {
	setup := setupSeries(rangeBar, purgeBar, setupBar(2, 100100, 100210, 100000, 100180))
	confirm := confirmSeries(confirmBar(0, 100030, 100250, 99950, 100100))

	result, _, err := Run(setup, confirm, testParams())
	if err != nil {
		t.Fatal(err)
	}
	if result.Funnel.StopLoss != 1 || result.Report.Losses != 1 {
		t.Fatalf("funnel %+v report %+v", result.Funnel, result.Report)
	}
	if result.Trades[0].NetRBps != -10000-769 {
		t.Fatalf("net R: got %d want %d", result.Trades[0].NetRBps, -10000-769)
	}
	if result.Trades[0].Outcome != crt.OutcomeStopLoss {
		t.Fatalf("outcome: got %s", result.Trades[0].Outcome)
	}
}

// A confirmation bar that closes at the same instant as a setup candle must be
// processed first, otherwise the setup candle would expire a limit that the
// bar actually filled.
func TestRunProcessesConfirmationBeforeSetupAtEqualTimestamps(t *testing.T) {
	setup := setupSeries(rangeBar, purgeBar, setupBar(2, 100100, 100210, 100000, 100180))
	// The only confirmation bar closes exactly at the expansion candle's close.
	confirm := confirmSeries(confirmBar(15, 100030, 100210, 100005, 100200))

	result, _, err := Run(setup, confirm, testParams())
	if err != nil {
		t.Fatal(err)
	}
	if result.Funnel.Expired != 0 {
		t.Fatalf("the limit must fill before the setup candle expires it: %+v", result.Funnel)
	}
	if result.Funnel.Filled != 1 || result.Funnel.TakeProfit != 1 {
		t.Fatalf("funnel: %+v", result.Funnel)
	}
}

func TestRunExpiresAnUnfilledLimit(t *testing.T) {
	setup := setupSeries(rangeBar, purgeBar, setupBar(2, 100100, 100190, 100090, 100180))
	confirm := confirmSeries(confirmBar(0, 100100, 100150, 100095, 100120))

	result, fills, err := Run(setup, confirm, testParams())
	if err != nil {
		t.Fatal(err)
	}
	if result.Funnel.Armed != 1 || result.Funnel.Expired != 1 || result.Funnel.Filled != 0 {
		t.Fatalf("funnel: %+v", result.Funnel)
	}
	if len(fills) != 0 || result.Report.Trades != 0 {
		t.Fatal("an expired setup must produce no fills and no trades")
	}
}

// Identical inputs must produce byte-identical results: the whole point of
// fixing the ordering and intrabar rules.
func TestRunIsDeterministic(t *testing.T) {
	setup := setupSeries(rangeBar, purgeBar, setupBar(2, 100100, 100210, 100000, 100180))
	confirm := confirmSeries(
		confirmBar(0, 100030, 100035, 100005, 100012),
		confirmBar(1, 100050, 100210, 100045, 100200),
	)

	encode := func() string {
		result, _, err := Run(setup, confirm, testParams())
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		return string(encoded)
	}
	first := encode()
	for i := 0; i < 20; i++ {
		if again := encode(); again != first {
			t.Fatalf("run %d diverged", i)
		}
	}
}

// Setup candles with no confirmation coverage can never fill. Booking them as
// unfilled expiries would corrupt the funnel, so they must be dropped and
// reported instead.
func TestRunDropsSetupBarsOutsideConfirmationCoverage(t *testing.T) {
	// Three leading setup bars precede any confirmation data.
	early := setupSeries(
		setupBar(-3, 100100, 100200, 100000, 100150),
		setupBar(-2, 100020, 100060, 99960, 100055),
		setupBar(-1, 100100, 100190, 100090, 100180),
		rangeBar, purgeBar, setupBar(2, 100100, 100210, 100000, 100180),
	)
	confirm := confirmSeries(
		confirmBar(0, 100030, 100035, 100005, 100012),
		confirmBar(1, 100050, 100210, 100045, 100200),
	)

	result, _, err := Run(early, confirm, testParams())
	if err != nil {
		t.Fatal(err)
	}
	if result.Funnel.Uncovered != 1 {
		t.Fatalf("uncovered: got %d want 1", result.Funnel.Uncovered)
	}
	if result.Funnel.Armed != 1 {
		t.Fatalf("armed: got %d want 1", result.Funnel.Armed)
	}
	// The uncovered pair must not appear as an expiry, and the covered pair
	// must still resolve normally.
	if result.Funnel.Expired != 0 {
		t.Fatalf("uncovered setups must not be booked as expiries: %+v", result.Funnel)
	}
	if result.Funnel.TakeProfit != 1 || result.Report.Trades != 1 {
		t.Fatalf("funnel %+v report %+v", result.Funnel, result.Report)
	}

	// An uncovered pattern must leave the machine able to arm the next one.
	if len(result.Trades) != 1 || result.Trades[0].Outcome != crt.OutcomeTakeProfit {
		t.Fatalf("trades: %+v", result.Trades)
	}
}

func TestRunRejectsIncoherentInput(t *testing.T) {
	setup := setupSeries(rangeBar, purgeBar)
	confirm := confirmSeries(confirmBar(0, 100030, 100035, 100005, 100012))

	mismatched := confirm
	mismatched.Timeframe = 30 * time.Minute
	if _, _, err := Run(setup, mismatched, testParams()); err == nil {
		t.Fatal("expected rejection of a confirmation timeframe mismatch")
	}

	otherSymbol := confirm
	otherSymbol.Symbol = "GBPUSD"
	if _, _, err := Run(setup, otherSymbol, testParams()); err == nil {
		t.Fatal("expected rejection of mismatched symbols")
	}

	unordered := setup
	unordered.Bars = []Bar{purgeBar, rangeBar}
	if _, _, err := Run(unordered, confirm, testParams()); err == nil {
		t.Fatal("expected rejection of an out-of-order setup series")
	}

	bad := testParams()
	bad.RiskMinorPerTrade = 0
	if _, _, err := Run(setup, confirm, bad); err == nil {
		t.Fatal("expected rejection of non-positive risk")
	}

	broken := setup
	broken.Bars = []Bar{{OpenTime: origin, Open: 100, High: 90, Low: 110, Close: 100, Ticks: 1}}
	if _, _, err := Run(broken, confirm, testParams()); err == nil {
		t.Fatal("expected rejection of invalid OHLC geometry")
	}
}
