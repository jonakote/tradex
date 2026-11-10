package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"tradex/internal/domain"
	"tradex/internal/execution"
	"tradex/internal/features"
	"tradex/internal/feed"
	"tradex/internal/instrument"
	"tradex/internal/notify"
	"tradex/internal/strategy/scan"
)

// WatchConfig configures the multi-strategy background monitor. Every enabled
// strategy is run against every instrument on each poll, and each setup is
// delivered as a signal tagged with the strategy that produced it.
type WatchConfig struct {
	Capital *feed.CapitalConfig `json:"capital"`
	// Strategies selects which detectors run, by name: "continuation",
	// "ict", "amn", "structuretrend", "trianglebreakout", "ibs". Empty
	// means all of them.
	Strategies []string `json:"strategies,omitempty"`
	// IBS parameters (used only when "ibs" is enabled).
	IBSHold          int   `json:"ibs_hold,omitempty"`
	IBSLongBelowBps  int64 `json:"ibs_long_below_bps,omitempty"`
	IBSShortAboveBps int64 `json:"ibs_short_above_bps,omitempty"`
	// IBSTrendFilterBars and IBSStrengthExitBps are the v0.2 overlay (see
	// docs/MEANREVERSION_SPEC.md): 0 on either disables it, reproducing
	// v0.1's raw signal exactly.
	IBSTrendFilterBars int   `json:"ibs_trend_filter_bars,omitempty"`
	IBSStrengthExitBps int64 `json:"ibs_strength_exit_bps,omitempty"`

	PollInterval string            `json:"poll_interval,omitempty"`
	Journal      string            `json:"journal,omitempty"`
	Desktop      bool              `json:"desktop,omitempty"`
	Telegram     bool              `json:"telegram,omitempty"`
	Blackouts    []watchBlackout   `json:"blackouts,omitempty"`
	Groups       map[string]string `json:"correlation_groups,omitempty"`
	// Cooldowns are Go durations, e.g. "4h".
	GroupCooldown  string `json:"group_cooldown,omitempty"`
	SymbolCooldown string `json:"symbol_cooldown,omitempty"`
}

// watchBlackout is the JSON form of notify.Blackout, durations as strings.
type watchBlackout struct {
	Name   string `json:"name"`
	At     string `json:"at"`
	Before string `json:"before,omitempty"`
	After  string `json:"after,omitempty"`
}

func (b watchBlackout) parse() (notify.Blackout, error) {
	at, err := time.Parse(time.RFC3339, b.At)
	if err != nil {
		return notify.Blackout{}, fmt.Errorf("blackout %q: bad time %q (want RFC3339): %w", b.Name, b.At, err)
	}
	before, err := parseDurationOr(b.Before, 30*time.Minute)
	if err != nil {
		return notify.Blackout{}, fmt.Errorf("blackout %q: %w", b.Name, err)
	}
	after, err := parseDurationOr(b.After, 60*time.Minute)
	if err != nil {
		return notify.Blackout{}, fmt.Errorf("blackout %q: %w", b.Name, err)
	}
	blackout := notify.Blackout{Name: b.Name, At: at.UTC(), Before: before, After: after}
	return blackout, blackout.Validate()
}

func parseDurationOr(text string, fallback time.Duration) (time.Duration, error) {
	if text == "" {
		return fallback, nil
	}
	return time.ParseDuration(text)
}

// buildDetectors returns the configured strategy set. Every one is currently
// UNVALIDATED (no positive out-of-sample result), reflected in each
// Detector's Validated() and stamped on every signal it emits.
func buildDetectors(cfg WatchConfig) ([]scan.Detector, error) {
	names := cfg.Strategies
	if len(names) == 0 {
		names = []string{"continuation", "ict", "amn", "structuretrend", "trianglebreakout", "ibs"}
	}
	var out []scan.Detector
	for _, name := range names {
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "continuation":
			out = append(out, scan.Continuation{Config: features.DefaultContinuationConfig()})
		case "ict":
			out = append(out, scan.ICT{Config: features.DefaultICTConfig()})
		case "amn":
			out = append(out, scan.AMN{Config: features.DefaultAMNConfig()})
		case "structuretrend":
			out = append(out, scan.StructureTrend{Config: features.DefaultStructureTrendConfig()})
		case "trianglebreakout":
			out = append(out, scan.TriangleBreakout{Config: features.DefaultTriangleBreakoutConfig()})
		case "ibs":
			long := cfg.IBSLongBelowBps
			if long == 0 && cfg.IBSShortAboveBps == 0 {
				long = 2000
			}
			out = append(out, scan.IBS{HoldBars: cfg.IBSHold, LongBelowBps: long, ShortAboveBps: cfg.IBSShortAboveBps,
				TrendFilterBars: cfg.IBSTrendFilterBars, StrengthExitBps: cfg.IBSStrengthExitBps})
		default:
			return nil, fmt.Errorf("unknown strategy %q", name)
		}
	}
	return out, nil
}

// runWatch polls market data and runs every enabled strategy against every
// instrument, delivering each setup as a strategy-tagged signal.
//
// It never places an order and holds no order authority. Every strategy here
// is UNVALIDATED: each delivered signal says so, because an unmeasured signal
// that looks authoritative is the most expensive output this system has.
func runWatch(args []string, stdout, stderr io.Writer) int {
	// --self-test delivers one obviously-fake signal through every sink.
	selfTest := false
	for len(args) > 0 && strings.HasPrefix(args[0], "-") {
		switch args[0] {
		case "--self-test", "-self-test":
			selfTest = true
		default:
			fmt.Fprintf(stderr, "tradex watch: unknown flag %q\n", args[0])
			return 2
		}
		args = args[1:]
	}
	if len(args) < 1 {
		fmt.Fprintln(stderr, "usage: tradex watch [--self-test] <config.json>")
		return 2
	}
	file, err := os.Open(args[0])
	if err != nil {
		fmt.Fprintf(stderr, "tradex watch: open config: %v\n", err)
		return 1
	}
	defer file.Close()

	var cfg WatchConfig
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		fmt.Fprintf(stderr, "tradex watch: parse config: %v\n", err)
		return 1
	}
	if cfg.Capital == nil {
		fmt.Fprintln(stderr, "tradex watch: a capital block is required")
		return 1
	}

	// Credentials come from the environment only.
	if cfg.Capital.APIKey == "" {
		cfg.Capital.APIKey = os.Getenv("CAPITAL_API_KEY")
	}
	if cfg.Capital.Identifier == "" {
		cfg.Capital.Identifier = os.Getenv("CAPITAL_IDENTIFIER")
	}
	if cfg.Capital.Password == "" {
		cfg.Capital.Password = os.Getenv("CAPITAL_PASSWORD")
	}
	if cfg.Capital.BaseURL == "" {
		cfg.Capital.BaseURL = os.Getenv("CAPITAL_BASE_URL")
	}

	detectors, err := buildDetectors(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "tradex watch: %v\n", err)
		return 1
	}
	maxRequired := 0
	for _, d := range detectors {
		if d.RequiredBars() > maxRequired {
			maxRequired = d.RequiredBars()
		}
	}

	source, err := feed.NewCapital(*cfg.Capital)
	if err != nil {
		fmt.Fprintf(stderr, "tradex watch: capital.com source: %v\n", err)
		return 1
	}
	timeframe := feed.IntervalDuration(source.Interval())
	if timeframe <= 0 {
		fmt.Fprintf(stderr, "tradex watch: unknown interval %q\n", source.Interval())
		return 1
	}
	pollInterval, err := parseDurationOr(cfg.PollInterval, timeframe)
	if err != nil {
		fmt.Fprintf(stderr, "tradex watch: poll_interval: %v\n", err)
		return 1
	}

	// Sinks. The journal is first and the only mandatory one: it is the
	// research record, not a notification.
	journalPath := cfg.Journal
	if journalPath == "" {
		journalPath = "data/signals.jsonl"
	}
	journal, err := notify.NewJournal(journalPath)
	if err != nil {
		fmt.Fprintf(stderr, "tradex watch: journal: %v\n", err)
		return 1
	}
	sinks := []notify.Sink{journal, notify.Writer{Out: stdout}}
	if cfg.Desktop {
		desktop := notify.Desktop{Urgency: "normal"}
		if !desktop.Available() {
			fmt.Fprintln(stderr, "tradex watch: notify-send not found; desktop notifications disabled")
		} else {
			sinks = append(sinks, desktop)
		}
	}
	telegramEnabled := false
	if cfg.Telegram {
		telegram := notify.Telegram{Token: os.Getenv("TELEGRAM_BOT_TOKEN"), ChatID: os.Getenv("TELEGRAM_CHAT_ID")}
		if !telegram.Configured() {
			fmt.Fprintln(stderr, "tradex watch: TELEGRAM_BOT_TOKEN/TELEGRAM_CHAT_ID not set; telegram disabled")
		} else {
			sinks = append(sinks, telegram)
			telegramEnabled = true
		}
	}
	delivery := notify.Multi{Sinks: sinks, OnError: func(sink string, err error) {
		fmt.Fprintf(stderr, "%s [notify:%s] %v\n", time.Now().UTC().Format(time.RFC3339), sink, err)
	}}

	gate := &notify.Gate{Groups: cfg.Groups}
	if gate.Groups == nil {
		gate.Groups = notify.DefaultCorrelationGroups()
	}
	if gate.GroupCooldown, err = parseDurationOr(cfg.GroupCooldown, 4*timeframe); err != nil {
		fmt.Fprintf(stderr, "tradex watch: group_cooldown: %v\n", err)
		return 1
	}
	if gate.SymbolCooldown, err = parseDurationOr(cfg.SymbolCooldown, 2*timeframe); err != nil {
		fmt.Fprintf(stderr, "tradex watch: symbol_cooldown: %v\n", err)
		return 1
	}
	for _, raw := range cfg.Blackouts {
		blackout, err := raw.parse()
		if err != nil {
			fmt.Fprintf(stderr, "tradex watch: %v\n", err)
			return 1
		}
		gate.Blackouts = append(gate.Blackouts, blackout)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-stop
		fmt.Fprintln(stderr, "\ntradex watch: shutting down")
		cancel()
	}()

	names := make([]string, len(detectors))
	for i, d := range detectors {
		names[i] = fmt.Sprintf("%s (%s)", d.Name(), d.Spec())
	}

	if selfTest {
		probe := notify.Signal{
			Validated: false, Strategy: "DELIVERY SELF-TEST — not a signal",
			SpecVersion: "n/a", Symbol: "EURUSD", Side: "sell", Timeframe: source.Interval(),
			EntryType: "LIMIT", Entry: "0.00000", Stop: "0.00000", Target: "0.00000", RewardRisk: "0.00",
			Reason:     "self-test of the notification chain; no market data was used",
			DetectedAt: time.Now().UTC(), EmittedAt: time.Now().UTC(),
		}
		if err := delivery.Send(ctx, probe); err != nil {
			fmt.Fprintf(stderr, "tradex watch: self-test delivery failed: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "self-test delivered to %d sinks\n", len(sinks))
		return 0
	}

	now := time.Now().UTC()
	fmt.Fprintln(stdout, "tradex watch — multi-strategy monitor")
	fmt.Fprintf(stdout, "  started      %s\n", now.Format("2006-01-02 15:04:05 UTC"))
	fmt.Fprintf(stdout, "  strategies   %s\n", strings.Join(names, ", "))
	fmt.Fprintln(stdout, "               ALL UNVALIDATED — no measured out-of-sample edge")
	fmt.Fprintf(stdout, "  instruments  %v @ %s\n", source.Symbols(), source.Interval())
	fmt.Fprintf(stdout, "  poll         every %s\n", pollInterval)
	fmt.Fprintf(stdout, "  journal      %s\n", journalPath)
	sinkNames := "stdout, journal"
	if cfg.Desktop {
		sinkNames += ", desktop"
	}
	if telegramEnabled {
		sinkNames += ", telegram"
	}
	fmt.Fprintf(stdout, "  sinks        %s\n", sinkNames)
	fmt.Fprintf(stdout, "  cooldowns    symbol %s, correlated group %s (per strategy)\n", gate.SymbolCooldown, gate.GroupCooldown)
	if next, ok := gate.NextBlackout(now); ok {
		fmt.Fprintf(stdout, "  next blackout %s at %s UTC (-%s/+%s)\n", next.Name, next.At.Format("2006-01-02 15:04"), next.Before, next.After)
	}
	fmt.Fprintln(stdout, "  no order authority: this process cannot place a trade")
	fmt.Fprintln(stdout)

	scanOnce := func() {
		started := time.Now()
		at := started.UTC()
		delivered := 0

		for _, symbol := range source.Symbols() {
			if ctx.Err() != nil {
				return
			}
			spec, ok := instrument.Lookup(symbol)
			if !ok {
				continue
			}
			// Ask for more bars than any detector requires so a weekend or
			// holiday cannot leave the window short.
			span := time.Duration(maxRequired*3) * timeframe
			to := time.Now().UTC()
			window, err := source.History(ctx, symbol, to.Add(-span), to)
			if err != nil {
				fmt.Fprintf(stderr, "%s [watch:%s] history: %v\n", at.Format(time.RFC3339), symbol, err)
				continue
			}
			// The spread the trade would actually pay, measured now. An
			// unavailable quote yields 0, which disables the cost floor
			// rather than assuming a favourable value.
			var spreadTicks int64
			if quote, err := source.Snapshot(ctx, symbol); err == nil {
				if s := int64(quote.Ask - quote.Bid); s > 0 {
					spreadTicks = s
				}
			}

			for _, det := range detectors {
				if len(window) < det.RequiredBars() {
					continue
				}
				setup, ok := det.Detect(spec, window, spreadTicks)
				if !ok {
					continue
				}
				if reason := gate.Allow(det.Name(), symbol, setup.DetectedAt, at); reason.Blocked {
					fmt.Fprintf(stdout, "%s [watch:%s/%s] suppressed — %s\n",
						at.Format(time.RFC3339), det.Name(), symbol, reason.Why)
					continue
				}
				sig := buildStrategySignal(det, setup, spec, source.Interval(), spreadTicks, at)
				if err := delivery.Send(ctx, sig); err != nil {
					fmt.Fprintf(stderr, "%s [watch:%s/%s] delivery: %v\n", at.Format(time.RFC3339), det.Name(), symbol, err)
				} else {
					delivered++
				}
			}
		}
		fmt.Fprintf(stdout, "%s [watch] scanned %d instruments x %d strategies in %s, %d delivered\n",
			at.Format(time.RFC3339), len(source.Symbols()), len(detectors),
			time.Since(started).Round(time.Millisecond), delivered)
	}

	scanOnce()
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			fmt.Fprintln(stdout, "tradex watch: stopped")
			return 0
		case <-ticker.C:
			scanOnce()
		}
	}
}

// buildStrategySignal converts a strategy-agnostic Setup into a deliverable,
// strategy-tagged signal.
func buildStrategySignal(det scan.Detector, setup scan.Setup, spec instrument.Spec,
	interval string, spreadTicks int64, emittedAt time.Time) notify.Signal {

	price := func(p domain.Price) string {
		if p == 0 {
			return "—"
		}
		return strconv.FormatFloat(spec.Price(int64(p)), 'f', spec.Digits, 64)
	}
	pips := func(ticks int64) string {
		if !spec.HasPipConvention() || ticks == 0 {
			return ""
		}
		return strconv.FormatFloat(float64(ticks)*spec.TickSize()/spec.PipSize, 'f', 1, 64)
	}

	sig := notify.Signal{
		Validated:   det.Validated(),
		Strategy:    det.Name(),
		SpecVersion: det.Spec(),
		Symbol:      spec.Symbol,
		Side:        setup.Side,
		Timeframe:   interval,
		EntryType:   setup.EntryType,
		Entry:       price(setup.Entry),
		Stop:        price(setup.Stop),
		Target:      price(setup.Target),
		SpreadPips:  pips(spreadTicks),
		Reason:      setup.Reason,
		DetectedAt:  setup.DetectedAt,
		EmittedAt:   emittedAt,
		Diagnostics: setup.Diagnostics,
	}
	if riskPct, ok := execution.HalfKellyRiskPct(spec.Symbol); ok {
		sig.SuggestedRiskPct = strconv.FormatFloat(riskPct, 'f', 2, 64)
	}
	if setup.StopTarget() {
		sig.RiskPips = pips(setup.RiskTicks)
		sig.RewardPips = pips(setup.Reward)
		if setup.RiskTicks > 0 {
			sig.RewardRisk = strconv.FormatFloat(float64(setup.Reward)/float64(setup.RiskTicks), 'f', 2, 64)
		}
	} else {
		sig.RewardRisk = "n/a (time exit)"
	}
	return sig
}
