// Package crtlab runs the Candle Range Theory state machine over historical
// candles and reports measured performance. It is research tooling: it builds
// synthetic fills for analytics and never touches a broker adapter.
//
// Event ordering is fixed so a run is reproducible: events are sorted by
// close time, and a confirmation bar that closes at the same instant as a
// setup candle is always processed first. Intrabar fills and exits therefore
// resolve before the setup candle that contains them can age or time-stop the
// position.
package crtlab

import (
	"errors"
	"fmt"
	"sort"
	"time"

	"tradex/internal/analytics"
	"tradex/internal/domain"
	"tradex/internal/features"
	"tradex/internal/strategy/crt"
)

// Bar is a minimal OHLC record in instrument ticks.
type Bar struct {
	OpenTime time.Time
	Open     domain.Price
	High     domain.Price
	Low      domain.Price
	Close    domain.Price
	Ticks    uint64
}

// Series is an ordered, single-symbol, single-timeframe candle stream.
type Series struct {
	Source    string
	Symbol    string
	Timeframe time.Duration
	Bars      []Bar
}

func (s Series) candle(bar Bar) domain.Candle {
	ticks := bar.Ticks
	if ticks == 0 {
		ticks = 1
	}
	return domain.Candle{
		Source: s.Source, Symbol: s.Symbol, Timeframe: s.Timeframe,
		OpenTime: bar.OpenTime.UTC(), CloseTime: bar.OpenTime.UTC().Add(s.Timeframe),
		Open: bar.Open, High: bar.High, Low: bar.Low, Close: bar.Close, TickCount: ticks,
	}
}

func (s Series) Validate() error {
	if s.Symbol == "" || s.Source == "" {
		return errors.New("series source and symbol are required")
	}
	if s.Timeframe <= 0 {
		return errors.New("series timeframe must be positive")
	}
	for i := range s.Bars {
		if err := s.candle(s.Bars[i]).Validate(); err != nil {
			return fmt.Errorf("bar %d (%s): %w", i, s.Bars[i].OpenTime.UTC(), err)
		}
		if i > 0 && !s.Bars[i].OpenTime.After(s.Bars[i-1].OpenTime) {
			return fmt.Errorf("bar %d is duplicate or out of order", i)
		}
	}
	return nil
}

// Costs are charged in ticks and are deducted from every closed trade:
// the spread once, plus slippage on entry and on exit.
type Costs struct {
	SpreadTicks   int64 `json:"spread_ticks"`
	SlippageTicks int64 `json:"slippage_ticks"`
}

func (c Costs) totalTicks() int64 { return c.SpreadTicks + 2*c.SlippageTicks }

func (c Costs) Validate() error {
	if c.SpreadTicks < 0 || c.SlippageTicks < 0 {
		return errors.New("costs cannot be negative")
	}
	return nil
}

// Params fixes everything a run depends on. Risk is constant per trade so
// that expectancy in minor units converts directly into expectancy in R.
type Params struct {
	Strategy           crt.Config `json:"strategy"`
	Costs              Costs      `json:"costs"`
	RiskMinorPerTrade  int64      `json:"risk_minor_per_trade"`
	InitialEquityMinor int64      `json:"initial_equity_minor"`
}

func DefaultParams() Params {
	return Params{
		Strategy:           crt.DefaultConfig(),
		Costs:              Costs{SpreadTicks: 2, SlippageTicks: 1},
		RiskMinorPerTrade:  10000,
		InitialEquityMinor: 1000000,
	}
}

func (p Params) Validate() error {
	if err := p.Strategy.Validate(); err != nil {
		return err
	}
	if err := p.Costs.Validate(); err != nil {
		return err
	}
	if p.RiskMinorPerTrade <= 0 {
		return errors.New("risk per trade must be positive")
	}
	if p.InitialEquityMinor <= 0 {
		return errors.New("initial equity must be positive")
	}
	return nil
}

// Funnel counts every opportunity state, so a flattering trade count cannot
// hide how many setups were discarded or never filled.
type Funnel struct {
	SetupBars int `json:"setup_bars"`
	Armed     int `json:"armed"`
	// Uncovered counts patterns skipped because the confirmation stream has
	// no data in their fill window. They are not trades and not expiries.
	Uncovered  int `json:"uncovered"`
	Filled     int `json:"filled"`
	Expired    int `json:"expired"`
	TakeProfit int `json:"take_profit"`
	StopLoss   int `json:"stop_loss"`
	TimeStop   int `json:"time_stop"`
}

// Bucket is a sliced sub-population of closed trades, in R basis points.
type Bucket struct {
	Trades        int   `json:"trades"`
	Wins          int   `json:"wins"`
	NetRBps       int64 `json:"net_r_bps"`
	ExpectancyBps int64 `json:"expectancy_r_bps"`
}

func (b *Bucket) add(netRBps int64) {
	b.Trades++
	if netRBps > 0 {
		b.Wins++
	}
	b.NetRBps += netRBps
	b.ExpectancyBps = b.NetRBps / int64(b.Trades)
}

// Trade is one closed position with costs already deducted.
type Trade struct {
	Symbol     string                   `json:"symbol"`
	Kind       features.CandleRangeKind `json:"kind"`
	Outcome    crt.Outcome              `json:"outcome"`
	DetectedAt time.Time                `json:"detected_at"`
	ResolvedAt time.Time                `json:"resolved_at"`
	GrossRBps  int64                    `json:"gross_r_bps"`
	NetRBps    int64                    `json:"net_r_bps"`
	PnLMinor   int64                    `json:"pnl_minor"`
}

type Result struct {
	Symbol          string             `json:"symbol"`
	SpecVersion     string             `json:"spec_version"`
	From            time.Time          `json:"from"`
	To              time.Time          `json:"to"`
	Funnel          Funnel             `json:"funnel"`
	Report          analytics.Report   `json:"report"`
	NetRBps         int64              `json:"net_r_bps"`
	ExpectancyRBps  int64              `json:"expectancy_r_bps"`
	MaxDrawdownRBps int64              `json:"max_drawdown_r_bps"`
	ByHour          map[int]*Bucket    `json:"by_setup_hour_utc"`
	ByKind          map[string]*Bucket `json:"by_kind"`
	Trades          []Trade            `json:"trades"`
}

type eventKind int

const (
	eventConfirmation eventKind = 0 // resolves first at an equal timestamp
	eventSetup        eventKind = 1
)

type event struct {
	at    time.Time
	kind  eventKind
	index int
}

// Run replays both streams through the real strategy machine. Trend context
// is derived from completed setup candles only, using the same swing detector
// the live pipeline would use, so enabling the trend gate introduces no
// look-ahead.
func Run(setup, confirmation Series, params Params) (Result, []domain.Fill, error) {
	if err := params.Validate(); err != nil {
		return Result{}, nil, err
	}
	if setup.Timeframe != params.Strategy.SetupTimeframe {
		return Result{}, nil, errors.New("setup series timeframe does not match strategy config")
	}
	if confirmation.Timeframe != params.Strategy.ConfirmationTimeframe {
		return Result{}, nil, errors.New("confirmation series timeframe does not match strategy config")
	}
	if setup.Symbol != confirmation.Symbol {
		return Result{}, nil, errors.New("setup and confirmation series describe different symbols")
	}
	if err := setup.Validate(); err != nil {
		return Result{}, nil, fmt.Errorf("setup series: %w", err)
	}
	if err := confirmation.Validate(); err != nil {
		return Result{}, nil, fmt.Errorf("confirmation series: %w", err)
	}
	if len(confirmation.Bars) == 0 || len(setup.Bars) == 0 {
		return Result{}, nil, errors.New("both series must contain bars")
	}
	// A pattern whose fill window has no confirmation data can never fill, so
	// arming it would silently book an unfilled expiry and corrupt the
	// opportunity funnel. Such patterns are counted as uncovered instead.
	coverFrom := confirmation.Bars[0].OpenTime.UTC()
	coverTo := confirmation.Bars[len(confirmation.Bars)-1].OpenTime.UTC().Add(confirmation.Timeframe)
	fillWindow := time.Duration(params.Strategy.EntryExpirySetupBars) * setup.Timeframe
	covered := func(detectedAt time.Time) bool {
		return coverTo.After(detectedAt) && coverFrom.Before(detectedAt.Add(fillWindow))
	}

	machine, err := crt.New(params.Strategy)
	if err != nil {
		return Result{}, nil, err
	}
	swings, err := features.NewSwingDetector(2, 2)
	if err != nil {
		return Result{}, nil, err
	}

	events := make([]event, 0, len(setup.Bars)+len(confirmation.Bars))
	for i, bar := range confirmation.Bars {
		events = append(events, event{at: bar.OpenTime.UTC().Add(confirmation.Timeframe), kind: eventConfirmation, index: i})
	}
	for i, bar := range setup.Bars {
		events = append(events, event{at: bar.OpenTime.UTC().Add(setup.Timeframe), kind: eventSetup, index: i})
	}
	sort.SliceStable(events, func(a, b int) bool {
		if events[a].at.Equal(events[b].at) {
			return events[a].kind < events[b].kind
		}
		return events[a].at.Before(events[b].at)
	})

	result := Result{
		Symbol:      setup.Symbol,
		SpecVersion: features.CandleRangeSpecVersion,
		ByHour:      map[int]*Bucket{},
		ByKind:      map[string]*Bucket{},
	}
	if len(events) > 0 {
		result.From, result.To = events[0].at, events[len(events)-1].at
	}

	var (
		fills      []domain.Fill
		confirmed  []features.Swing
		equityRBps int64
		peakRBps   int64
		sequence   int
	)

	record := func(resolution crt.Resolution) {
		switch resolution.Outcome {
		case crt.OutcomeExpired:
			result.Funnel.Expired++
			machine.Reset()
			return
		case crt.OutcomeTakeProfit:
			result.Funnel.TakeProfit++
		case crt.OutcomeStopLoss:
			result.Funnel.StopLoss++
		case crt.OutcomeTimeStop:
			result.Funnel.TimeStop++
		}

		risk := resolution.Pattern.RiskTicks
		costRBps := int64(0)
		if risk > 0 {
			costRBps = params.Costs.totalTicks() * 10000 / risk
		}
		netRBps := resolution.RMultipleBps - costRBps
		pnlMinor := netRBps * params.RiskMinorPerTrade / 10000

		sequence++
		base := domain.Fill{
			FillID: fmt.Sprintf("%s-%d", setup.Symbol, sequence), Symbol: setup.Symbol,
			AccountID: "crtlab", Quantity: 1,
		}
		entry := base
		entry.Role = domain.FillEntry
		entry.Price = resolution.EntryPrice
		entry.FilledAt = resolution.FilledAt
		closing := base
		closing.Role = domain.FillTakeProfit
		if netRBps < 0 {
			closing.Role = domain.FillStopLoss
		}
		closing.Price = resolution.ExitPrice
		closing.FilledAt = resolution.ResolvedAt
		closing.ClosedTradeNetMinor = pnlMinor
		closing.RealizedNetMinor = pnlMinor
		fills = append(fills, entry, closing)

		result.NetRBps += netRBps
		equityRBps += netRBps
		if equityRBps > peakRBps {
			peakRBps = equityRBps
		}
		if drawdown := peakRBps - equityRBps; drawdown > result.MaxDrawdownRBps {
			result.MaxDrawdownRBps = drawdown
		}

		hour := resolution.Pattern.DetectedAt.UTC().Hour()
		if result.ByHour[hour] == nil {
			result.ByHour[hour] = &Bucket{}
		}
		result.ByHour[hour].add(netRBps)
		kind := string(resolution.Pattern.Kind)
		if result.ByKind[kind] == nil {
			result.ByKind[kind] = &Bucket{}
		}
		result.ByKind[kind].add(netRBps)

		result.Trades = append(result.Trades, Trade{
			Symbol: setup.Symbol, Kind: resolution.Pattern.Kind, Outcome: resolution.Outcome,
			DetectedAt: resolution.Pattern.DetectedAt, ResolvedAt: resolution.ResolvedAt,
			GrossRBps: resolution.RMultipleBps, NetRBps: netRBps, PnLMinor: pnlMinor,
		})
		machine.Reset()
	}

	for _, ev := range events {
		switch ev.kind {
		case eventConfirmation:
			before := machine.State()
			if before != crt.Armed && before != crt.Filled {
				continue
			}
			candle := confirmation.candle(confirmation.Bars[ev.index])
			resolution, err := machine.OnConfirmationCandle(candle)
			if err != nil {
				return Result{}, nil, fmt.Errorf("confirmation %s: %w", candle.CloseTime, err)
			}
			// A limit can only ever fill out of ARMED, so counting the
			// transition once is exact even when the same bar also exits.
			if before == crt.Armed && machine.State() != crt.Armed {
				result.Funnel.Filled++
			}
			if resolution != nil {
				record(*resolution)
			}
		case eventSetup:
			candle := setup.candle(setup.Bars[ev.index])
			result.Funnel.SetupBars++

			newSwings, err := swings.Push(candle)
			if err != nil {
				return Result{}, nil, fmt.Errorf("swing detector %s: %w", candle.CloseTime, err)
			}
			confirmed = append(confirmed, newSwings...)
			if machine.State() == crt.SeekingRange && params.Strategy.RequireTrendAlignment {
				if err := machine.SetContext(features.ClassifyTrend(confirmed)); err != nil {
					return Result{}, nil, err
				}
			}

			armed, resolution, err := machine.OnSetupCandle(candle)
			if err != nil {
				return Result{}, nil, fmt.Errorf("setup %s: %w", candle.CloseTime, err)
			}
			if armed != nil {
				// Patterns the confirmation stream cannot resolve are not
				// opportunities; stand the machine down without booking them.
				if !covered(armed.Pattern.DetectedAt) {
					result.Funnel.Uncovered++
					if err := machine.StandDown("confirmation stream has no data in the fill window"); err != nil {
						return Result{}, nil, err
					}
					continue
				}
				result.Funnel.Armed++
			}
			if resolution != nil {
				record(*resolution)
			}
		}
	}

	report, err := analytics.BuildReport(fills, params.InitialEquityMinor)
	if err != nil {
		return Result{}, nil, err
	}
	result.Report = report
	if report.Trades > 0 {
		result.ExpectancyRBps = result.NetRBps / int64(report.Trades)
	}
	return result, fills, nil
}
