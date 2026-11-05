package crt

import (
	"testing"
	"time"

	"tradex/internal/domain"
	"tradex/internal/features"
)

var origin = time.Date(2026, time.August, 18, 0, 0, 0, 0, time.UTC)

func candle(openTime time.Time, timeframe time.Duration, open, high, low, close domain.Price) domain.Candle {
	return domain.Candle{
		Source: "test", Symbol: "EURUSD", Timeframe: timeframe,
		OpenTime: openTime, CloseTime: openTime.Add(timeframe),
		Open: open, High: high, Low: low, Close: close, TickCount: 100,
	}
}

func setupCandle(index int, open, high, low, close domain.Price) domain.Candle {
	return candle(origin.Add(time.Duration(index)*4*time.Hour), 4*time.Hour, open, high, low, close)
}

func confirmCandle(index int, open, high, low, close domain.Price) domain.Candle {
	return candle(origin.Add(8*time.Hour).Add(time.Duration(index)*15*time.Minute),
		15*time.Minute, open, high, low, close)
}

// armBullish drives a machine to ARMED on the canonical bullish pattern:
// range 100000..100200, low purged to 99960, entry 100010, stop 99958,
// target 100200, risk 52 ticks.
func armBullish(t *testing.T, config Config) *Machine {
	t.Helper()
	machine, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := machine.OnSetupCandle(setupCandle(0, 100050, 100200, 100000, 100150)); err != nil {
		t.Fatal(err)
	}
	setup, _, err := machine.OnSetupCandle(setupCandle(1, 100020, 100060, 99960, 100055))
	if err != nil {
		t.Fatal(err)
	}
	if setup == nil {
		t.Fatal("expected the purge candle to arm a setup")
	}
	if machine.State() != Armed {
		t.Fatalf("state: got %s want ARMED", machine.State())
	}
	if setup.Pattern.EntryReference != 100010 || setup.Pattern.StopReference != 99958 ||
		setup.Pattern.TargetReference != 100200 {
		t.Fatalf("unexpected levels: %+v", setup.Pattern)
	}
	return machine
}

func TestMachineFillsAtEquilibriumThenReachesTarget(t *testing.T) {
	machine := armBullish(t, DefaultConfig())

	// Trades into the equilibrium without threatening the stop.
	resolution, err := machine.OnConfirmationCandle(confirmCandle(0, 100030, 100035, 100005, 100012))
	if err != nil {
		t.Fatal(err)
	}
	if resolution != nil {
		t.Fatalf("premature resolution: %+v", resolution)
	}
	if machine.State() != Filled {
		t.Fatalf("state: got %s want FILLED", machine.State())
	}

	resolution, err = machine.OnConfirmationCandle(confirmCandle(1, 100050, 100210, 100045, 100200))
	if err != nil {
		t.Fatal(err)
	}
	if resolution == nil {
		t.Fatal("expected a resolution")
	}
	if resolution.Outcome != OutcomeTakeProfit {
		t.Fatalf("outcome: got %s", resolution.Outcome)
	}
	if resolution.ExitPrice != 100200 || resolution.EntryPrice != 100010 {
		t.Fatalf("prices: entry %d exit %d", resolution.EntryPrice, resolution.ExitPrice)
	}
	// (100200-100010)/52 = 3.653R
	if resolution.RMultipleBps != 36538 {
		t.Fatalf("R multiple: got %d want 36538", resolution.RMultipleBps)
	}
	if machine.State() != Completed {
		t.Fatalf("state: got %s want COMPLETED", machine.State())
	}
}

// The determinism rule that decides whether a backtest is honest: when one
// confirmation bar contains both the stop and the target, the stop wins.
func TestMachineResolvesAmbiguousBarToTheStop(t *testing.T) {
	machine := armBullish(t, DefaultConfig())

	resolution, err := machine.OnConfirmationCandle(confirmCandle(0, 100030, 100250, 99950, 100100))
	if err != nil {
		t.Fatal(err)
	}
	if resolution == nil {
		t.Fatal("expected a resolution")
	}
	if resolution.Outcome != OutcomeStopLoss {
		t.Fatalf("an ambiguous bar must resolve to the stop, got %s", resolution.Outcome)
	}
	if resolution.RMultipleBps != -10000 {
		t.Fatalf("R multiple: got %d want -10000", resolution.RMultipleBps)
	}
}

func TestMachineExpiresAnUnfilledLimitOnTheExpansionClose(t *testing.T) {
	machine := armBullish(t, DefaultConfig())

	// The expansion candle completes without ever trading to equilibrium.
	_, resolution, err := machine.OnSetupCandle(setupCandle(2, 100100, 100190, 100090, 100180))
	if err != nil {
		t.Fatal(err)
	}
	if resolution == nil {
		t.Fatal("expected an expiry resolution")
	}
	if resolution.Outcome != OutcomeExpired || resolution.RMultipleBps != 0 {
		t.Fatalf("outcome: got %s (%d bps)", resolution.Outcome, resolution.RMultipleBps)
	}
	if machine.State() != Expired {
		t.Fatalf("state: got %s want EXPIRED", machine.State())
	}
}

func TestMachineAppliesTimeStopAtSetupClose(t *testing.T) {
	machine := armBullish(t, DefaultConfig())
	if _, err := machine.OnConfirmationCandle(confirmCandle(0, 100030, 100035, 100005, 100012)); err != nil {
		t.Fatal(err)
	}

	// Expiry budget (1) plus hold budget (2) = 3 setup closes after arming.
	for index := 2; index <= 3; index++ {
		_, resolution, err := machine.OnSetupCandle(setupCandle(index, 100020, 100100, 100000, 100060))
		if err != nil {
			t.Fatal(err)
		}
		if resolution != nil {
			t.Fatalf("setup bar %d resolved too early: %+v", index, resolution)
		}
	}
	_, resolution, err := machine.OnSetupCandle(setupCandle(4, 100060, 100100, 100020, 100062))
	if err != nil {
		t.Fatal(err)
	}
	if resolution == nil {
		t.Fatal("expected a time stop")
	}
	if resolution.Outcome != OutcomeTimeStop {
		t.Fatalf("outcome: got %s", resolution.Outcome)
	}
	// Closed at 100062 from an entry of 100010 over 52 ticks of risk = +1R.
	if resolution.RMultipleBps != 10000 {
		t.Fatalf("R multiple: got %d want 10000", resolution.RMultipleBps)
	}
}

func TestMachineTrendGateBlocksCounterTrendSetups(t *testing.T) {
	config := DefaultConfig()
	config.RequireTrendAlignment = true

	machine, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := machine.SetContext(features.TrendBearish); err != nil {
		t.Fatal(err)
	}
	if _, _, err := machine.OnSetupCandle(setupCandle(0, 100050, 100200, 100000, 100150)); err != nil {
		t.Fatal(err)
	}
	setup, _, err := machine.OnSetupCandle(setupCandle(1, 100020, 100060, 99960, 100055))
	if err != nil {
		t.Fatal(err)
	}
	if setup != nil {
		t.Fatal("a bullish range must not arm against bearish context")
	}
	if machine.State() != SeekingRange {
		t.Fatalf("state: got %s want SEEKING_RANGE", machine.State())
	}

	aligned := armBullishWithTrend(t, config, features.TrendBullish)
	if aligned.State() != Armed {
		t.Fatalf("aligned context must arm, got %s", aligned.State())
	}
}

func armBullishWithTrend(t *testing.T, config Config, trend features.Trend) *Machine {
	t.Helper()
	machine, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := machine.SetContext(trend); err != nil {
		t.Fatal(err)
	}
	if _, _, err := machine.OnSetupCandle(setupCandle(0, 100050, 100200, 100000, 100150)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := machine.OnSetupCandle(setupCandle(1, 100020, 100060, 99960, 100055)); err != nil {
		t.Fatal(err)
	}
	return machine
}

func TestMachineRejectsOutOfContractInputs(t *testing.T) {
	machine, err := New(DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	// No pattern is armed, so no confirmation bar can be interpreted.
	if _, err := machine.OnConfirmationCandle(confirmCandle(0, 100030, 100035, 100005, 100012)); err == nil {
		t.Fatal("expected rejection of a confirmation candle while seeking a range")
	}

	armed := armBullish(t, DefaultConfig())
	if _, err := armed.OnConfirmationCandle(setupCandle(2, 100020, 100100, 100000, 100060)); err == nil {
		t.Fatal("expected rejection of a setup-timeframe candle as confirmation")
	}
	// A bar that closes at or before the arming instant would be look-ahead.
	stale := candle(origin.Add(7*time.Hour+45*time.Minute), 15*time.Minute, 100030, 100035, 100005, 100012)
	if _, err := armed.OnConfirmationCandle(stale); err == nil {
		t.Fatal("expected rejection of a confirmation candle that predates the pattern")
	}
	if err := armed.SetContext(features.TrendBullish); err == nil {
		t.Fatal("expected context to be locked once armed")
	}
	if _, _, err := armed.OnSetupCandle(setupCandle(1, 100020, 100060, 99960, 100055)); err == nil {
		t.Fatal("expected rejection of a duplicate setup candle")
	}
}

func TestConfigValidationRejectsIncoherentTimeframes(t *testing.T) {
	if err := DefaultConfig().Validate(); err != nil {
		t.Fatalf("default config must be valid: %v", err)
	}
	cases := map[string]func(*Config){
		"inverted timeframes": func(c *Config) { c.ConfirmationTimeframe = 8 * time.Hour },
		"non multiple":        func(c *Config) { c.ConfirmationTimeframe = 70 * time.Minute },
		"zero expiry":         func(c *Config) { c.EntryExpirySetupBars = 0 },
		"zero hold":           func(c *Config) { c.MaxHoldSetupBars = 0 },
		"bad detector":        func(c *Config) { c.Detector.MinRangeTicks = 0 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			config := DefaultConfig()
			mutate(&config)
			if err := config.Validate(); err == nil {
				t.Fatal("expected a validation error")
			}
			if _, err := New(config); err == nil {
				t.Fatal("expected construction to fail")
			}
		})
	}
}

func TestStandDownCancelsAnArmedSetupWithoutATrade(t *testing.T) {
	machine := armBullish(t, DefaultConfig())
	if err := machine.StandDown(""); err == nil {
		t.Fatal("expected a reason to be required")
	}
	if err := machine.StandDown("no confirmation data"); err != nil {
		t.Fatal(err)
	}
	if machine.State() != SeekingRange {
		t.Fatalf("state: got %s want SEEKING_RANGE", machine.State())
	}
	if err := machine.StandDown("again"); err == nil {
		t.Fatal("expected rejection of stand-down while not armed")
	}
	var sawInvalidation bool
	for _, transition := range machine.Transitions() {
		if transition.To == Invalidated && transition.Reason == "no confirmation data" {
			sawInvalidation = true
		}
	}
	if !sawInvalidation {
		t.Fatal("stand-down must be recorded in the audit trail")
	}
}

func TestResetReturnsATerminalMachineToSeeking(t *testing.T) {
	machine := armBullish(t, DefaultConfig())
	if _, err := machine.OnConfirmationCandle(confirmCandle(0, 100030, 100250, 99950, 100100)); err != nil {
		t.Fatal(err)
	}
	if machine.State() != Completed {
		t.Fatalf("state: got %s", machine.State())
	}
	machine.Reset()
	if machine.State() != SeekingRange {
		t.Fatalf("state after reset: got %s", machine.State())
	}
	if machine.Pattern().Kind != "" {
		t.Fatal("reset must clear the armed pattern")
	}
	if len(machine.Transitions()) == 0 {
		t.Fatal("reset must preserve the audit trail")
	}
}
