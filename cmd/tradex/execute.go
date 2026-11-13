// execute.go: `tradex execute <config.json>` — turns a MEASURED IBS signal
// into a real (demo-account) Capital.com order, with every entry gated by
// internal/execution.RiskManager and every order carrying a mandatory
// broker-side protective stop.
//
// Hard boundaries, on purpose:
//   - internal/feed stays read-only; this file never touches it for
//     orders, only for candles/quotes (same as watch.go).
//   - internal/broker refuses to construct against anything but a demo
//     URL — see internal/broker/capital.go.
//   - only symbols present in execution.MeasuredIBSStats are ever traded;
//     an instrument with no measured size entry gets no order, full stop.
//   - every entry is still delivered through the exact same notify.Multi
//     sinks watch.go uses, so Telegram/journal visibility doesn't change.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"tradex/internal/broker"
	"tradex/internal/domain"
	"tradex/internal/execution"
	"tradex/internal/features"
	"tradex/internal/feed"
	"tradex/internal/instrument"
	"tradex/internal/notify"
	"tradex/internal/strategy/scan"
)

// marginFactorPct is the broker's margin requirement as a percent of
// notional, verified live via GET /api/v1/markets/{epic} on 2026-11-11 for
// exactly the instruments this command is allowed to trade. An instrument
// missing here cannot be sized and will be skipped, not guessed.
var marginFactorPct = map[string]float64{
	"US100":    5,
	"US500":    5,
	"PLATINUM": 10,
	"EURUSD":   3.333333,
	"NATGAS":   10,
	"XAUUSD":   5,
	"GBPUSD":   3.333333,
	"BTCUSD":   50,
	"J225":     5,
	"WTI":      10,
	"BRENT":    10,
	"COPPER":   10,
	"US30":     5,
	"USDCAD":   3.333333,
	"AUDJPY":   5,
	"EURJPY":   3.333333,
	"EURGBP":   3.333333,
	"GBPJPY":   3.333333,
	"AUDUSD":   5,
}

type openTrade struct {
	Symbol               string    `json:"symbol"`
	DealID               string    `json:"deal_id"`
	Side                 string    `json:"side"` // "buy"/"sell"
	EntryPrice           float64   `json:"entry_price"`
	Size                 float64   `json:"size"`
	MarginPct            float64   `json:"margin_pct"`
	HoldExitRule         string    `json:"hold_exit_rule"`
	HoldExitThresholdBps int64     `json:"hold_exit_threshold_bps"`
	MaxHoldBars          int       `json:"max_hold_bars"`
	BarsHeld             int       `json:"bars_held"`
	LastBarSeen          time.Time `json:"last_bar_seen"`
	OpenedAt             time.Time `json:"opened_at"`
}

type openTradesFile struct {
	Trades map[string]openTrade `json:"trades"` // keyed by symbol
}

func loadOpenTrades(path string) openTradesFile {
	data, err := os.ReadFile(path)
	if err != nil {
		return openTradesFile{Trades: map[string]openTrade{}}
	}
	var f openTradesFile
	if json.Unmarshal(data, &f) != nil || f.Trades == nil {
		return openTradesFile{Trades: map[string]openTrade{}}
	}
	return f
}

func saveOpenTrades(path string, f openTradesFile) error {
	if dir := filepath.Dir(path); dir != "." {
		os.MkdirAll(dir, 0o755)
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func runExecute(args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		fmt.Fprintln(stderr, "usage: tradex execute <config.json>")
		return 2
	}
	file, err := os.Open(args[0])
	if err != nil {
		fmt.Fprintf(stderr, "tradex execute: open config: %v\n", err)
		return 1
	}
	var cfg WatchConfig
	dec := json.NewDecoder(file)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		file.Close()
		fmt.Fprintf(stderr, "tradex execute: parse config: %v\n", err)
		return 1
	}
	file.Close()
	if cfg.Capital == nil {
		fmt.Fprintln(stderr, "tradex execute: a capital block is required")
		return 1
	}
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

	bro, err := broker.NewClient(broker.Config{
		APIKey: cfg.Capital.APIKey, Identifier: cfg.Capital.Identifier,
		Password: cfg.Capital.Password, BaseURL: cfg.Capital.BaseURL,
	})
	if err != nil {
		fmt.Fprintf(stderr, "tradex execute: %v\n", err) // includes the demo-URL refusal message
		return 1
	}

	detectors, err := buildDetectors(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "tradex execute: %v\n", err)
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
		fmt.Fprintf(stderr, "tradex execute: capital.com source: %v\n", err)
		return 1
	}
	timeframe := feed.IntervalDuration(source.Interval())
	pollInterval, err := parseDurationOr(cfg.PollInterval, timeframe)
	if err != nil {
		fmt.Fprintf(stderr, "tradex execute: poll_interval: %v\n", err)
		return 1
	}

	journalPath := cfg.Journal
	if journalPath == "" {
		journalPath = "data/signals.jsonl"
	}
	journal, err := notify.NewJournal(journalPath)
	if err != nil {
		fmt.Fprintf(stderr, "tradex execute: journal: %v\n", err)
		return 1
	}
	sinks := []notify.Sink{journal, notify.Writer{Out: stdout}}
	if cfg.Telegram {
		telegram := notify.Telegram{Token: os.Getenv("TELEGRAM_BOT_TOKEN"), ChatID: os.Getenv("TELEGRAM_CHAT_ID")}
		if telegram.Configured() {
			sinks = append(sinks, telegram)
		}
	}
	delivery := notify.Multi{Sinks: sinks, OnError: func(sink string, err error) {
		fmt.Fprintf(stderr, "%s [notify:%s] %v\n", time.Now().UTC().Format(time.RFC3339), sink, err)
	}}

	execJournalPath := "data/demo_executions.jsonl"
	if dir := filepath.Dir(execJournalPath); dir != "." {
		os.MkdirAll(dir, 0o755)
	}
	logExec := func(event map[string]any) {
		event["at"] = time.Now().UTC().Format(time.RFC3339)
		b, _ := json.Marshal(event)
		fmt.Fprintln(stdout, string(b))
		f, err := os.OpenFile(execJournalPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			fmt.Fprintf(stderr, "exec journal: %v\n", err)
			return
		}
		defer f.Close()
		fmt.Fprintln(f, string(b))
	}

	openTradesPath := "data/demo_open_trades.json"
	lockPath := "data/demo_execute.lock"
	risk, err := execution.NewRiskManager(execution.DefaultRiskConfig())
	if err != nil {
		fmt.Fprintf(stderr, "tradex execute: risk manager: %v\n", err)
		return 1
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	go func() { <-stop; cancel() }()

	fmt.Fprintln(stdout, "tradex execute — DEMO ACCOUNT order placement")
	fmt.Fprintf(stdout, "  base_url     %s\n", cfg.Capital.BaseURL)
	fmt.Fprintf(stdout, "  instruments  %v @ %s\n", source.Symbols(), source.Interval())
	fmt.Fprintf(stdout, "  tradeable    %v (only symbols with a measured size entry)\n", tradeableSymbols(source.Symbols()))
	fmt.Fprintln(stdout, "  every order carries a mandatory broker-side protective stop")
	fmt.Fprintln(stdout, "  risk: 2% max per trade, 4 max open positions, 25% max margin, 5%/9% soft/hard daily loss cap")
	fmt.Fprintln(stdout)

	closeAndRecord := func(symbol string, t openTrade, reason string, trades openTradesFile) {
		if err := bro.ClosePosition(t.DealID); err != nil {
			fmt.Fprintf(stderr, "%s [execute:%s] close failed: %v\n", time.Now().UTC().Format(time.RFC3339), symbol, err)
			return
		}
		window, err := source.History(ctx, symbol, time.Now().UTC().Add(-3*timeframe), time.Now().UTC())
		exitPrice := t.EntryPrice
		if err == nil && len(window) > 0 {
			spec, _ := instrument.Lookup(symbol)
			exitPrice = spec.Price(int64(window[len(window)-1].Close))
		}
		sign := 1.0
		if t.Side == "sell" {
			sign = -1.0
		}
		pnl := sign * (exitPrice - t.EntryPrice) * t.Size
		risk.RecordClose(pnl)
		logExec(map[string]any{"event": "close", "symbol": symbol, "deal_id": t.DealID, "side": t.Side,
			"entry": t.EntryPrice, "exit": exitPrice, "size": t.Size, "pnl": pnl, "reason": reason})
		delete(trades.Trades, symbol)
	}

	scanOnce := func() {
		lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
		if err != nil {
			fmt.Fprintf(stderr, "tradex execute: lock: %v\n", err)
			return
		}
		defer lockFile.Close()
		// Two `execute` processes (one per config) share the SAME state
		// files on purpose, so risk limits are enforced across the whole
		// account, not per-process. That means their read-modify-write of
		// those files must be serialized, or one process's save can
		// silently erase the other's just-written position. This lock is
		// the fix for exactly that race.
		if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX); err != nil {
			fmt.Fprintf(stderr, "tradex execute: flock: %v\n", err)
			return
		}
		defer syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)

		trades := loadOpenTrades(openTradesPath)

		// Reconcile against the broker's actual open positions before
		// doing anything else. If a position exists on Capital.com for one
		// of our symbols that this process's local state doesn't know
		// about (e.g. a crash between PlaceOrder succeeding and
		// saveOpenTrades running), ADOPT it into local state rather than
		// risk placing a second, duplicate order for the same symbol.
		if positions, err := bro.OpenPositions(); err == nil {
			byEpic := map[string]broker.Position{}
			for _, p := range positions {
				byEpic[p.Epic] = p
			}
			for symbol := range marginFactorPct {
				if _, tracked := trades.Trades[symbol]; tracked {
					continue
				}
				epic, _ := instrument.CapitalEpic(symbol)
				if epic == "" {
					epic = symbol
				}
				p, found := byEpic[epic]
				if !found {
					continue
				}
				fmt.Fprintf(stderr, "%s [execute:%s] RECONCILED an untracked open broker position (dealId=%s) — adopting it instead of risking a duplicate order\n",
					time.Now().UTC().Format(time.RFC3339), symbol, p.DealID)
				side := "buy"
				if p.Direction == "SELL" {
					side = "sell"
				}
				marginFactor := marginFactorPct[symbol]
				trades.Trades[symbol] = openTrade{
					Symbol: symbol, DealID: p.DealID, Side: side, EntryPrice: p.Level, Size: p.Size,
					MarginPct: p.Size * p.Level * marginFactor / 100, // equity unknown here; recomputed next loop anyway
					MaxHoldBars: 10, LastBarSeen: time.Now().UTC(), OpenedAt: time.Now().UTC(),
				}
			}
			saveOpenTrades(openTradesPath, trades)
		}

		// Daily hard-loss flatten check, before anything else.
		if acct, err := bro.AccountSummary(); err == nil && risk.ShouldFlattenEverything(acct.Balance) {
			logExec(map[string]any{"event": "HARD_DAILY_LOSS_CAP_BREACHED", "action": "flattening all open positions", "balance": acct.Balance})
			for symbol, t := range trades.Trades {
				closeAndRecord(symbol, t, "hard_daily_loss_cap", trades)
			}
			saveOpenTrades(openTradesPath, trades)
			return
		}

		// Manage existing positions: exit-on-condition or max-hold.
		for symbol, t := range trades.Trades {
			_, ok := instrument.Lookup(symbol)
			if !ok {
				continue
			}
			window, err := source.History(ctx, symbol, time.Now().UTC().Add(-time.Duration(maxRequired*3)*timeframe), time.Now().UTC())
			if err != nil || len(window) == 0 {
				continue
			}
			last := window[len(window)-1]
			if !last.CloseTime.After(t.LastBarSeen) {
				continue // no new closed bar yet
			}
			t.BarsHeld++
			t.LastBarSeen = last.CloseTime
			trades.Trades[symbol] = t

			exit := t.BarsHeld >= t.MaxHoldBars
			if !exit && t.HoldExitRule == "confluence_strength_above" {
				// OR of both oscillators against their own strategy's
				// threshold -- the only kind of exit confluence trades
				// use, so hardcoded rather than routed through the
				// generic single-value dispatch below.
				if ibs, ok := currentOscillatorBps("ibs_strength_above", window); ok && ibs >= 5000 {
					exit = true
				}
				if rsi2, ok := currentOscillatorBps("rsi2_strength_above", window); ok && rsi2 >= 7000 {
					exit = true
				}
			}
			if !exit && t.HoldExitRule != "" && t.HoldExitRule != "confluence_strength_above" {
				if osc, ok := currentOscillatorBps(t.HoldExitRule, window); ok {
					if strings.HasSuffix(t.HoldExitRule, "_strength_above") && osc >= t.HoldExitThresholdBps {
						exit = true
					}
					if strings.HasSuffix(t.HoldExitRule, "_strength_below") && osc <= t.HoldExitThresholdBps {
						exit = true
					}
				}
			}
			if exit {
				closeAndRecord(symbol, t, "strategy_exit", trades)
			}
		}
		saveOpenTrades(openTradesPath, trades)

		// Look for new entries, only on tradeable (measured) symbols with
		// no position already open.
		acct, err := bro.AccountSummary()
		if err != nil {
			fmt.Fprintf(stderr, "%s [execute] account summary: %v\n", time.Now().UTC().Format(time.RFC3339), err)
			return
		}
		committedMarginPct := 0.0
		for _, t := range trades.Trades {
			committedMarginPct += t.MarginPct
		}

		for _, symbol := range source.Symbols() {
			if ctx.Err() != nil {
				return
			}
			if _, open := trades.Trades[symbol]; open {
				continue
			}
			marginFactor, haveMargin := marginFactorPct[symbol]
			if !haveMargin {
				continue
			}
			spec, ok := instrument.Lookup(symbol)
			if !ok {
				continue
			}
			window, err := source.History(ctx, symbol, time.Now().UTC().Add(-time.Duration(maxRequired*3)*timeframe), time.Now().UTC())
			if err != nil || len(window) < maxRequired {
				continue
			}
			var spreadTicks int64
			if quote, err := source.Snapshot(ctx, symbol); err == nil {
				if s := int64(quote.Ask - quote.Bid); s > 0 {
					spreadTicks = s
				}
			}
			var ibsSetup, rsi2Setup scan.Setup
			var ibsOK, rsi2OK bool
			for _, det := range detectors {
				name := strings.ToUpper(det.Name())
				if (name != "IBS" && name != "RSI2") || len(window) < det.RequiredBars() {
					continue
				}
				if s, ok := det.Detect(spec, window, spreadTicks); ok {
					if name == "IBS" {
						ibsSetup, ibsOK = s, true
					} else {
						rsi2Setup, rsi2OK = s, true
					}
				}
			}

			var setup scan.Setup
			var found bool
			var matchedStrategy string
			switch {
			// CONFLUENCE: IBS and RSI2 are NOT individually confirmed on
			// these symbols (XAUUSD/GBPUSD) — only trade when both fire
			// on the same symbol, same day, same side.
			case execution.ConfluenceOnlySymbols[symbol]:
				if ibsOK && rsi2OK && ibsSetup.Side == rsi2Setup.Side {
					setup, found, matchedStrategy = ibsSetup, true, "CONFLUENCE"
				}
			// CONFLUENCE BOOST: IBS and RSI2 are ALREADY independently
			// confirmed on these symbols (US100/PLATINUM) — a
			// single-signal day still trades normally; agreement just
			// gets a separately-measured, stronger size on top.
			case execution.ConfluenceBoostSymbols[symbol] && ibsOK && rsi2OK && ibsSetup.Side == rsi2Setup.Side:
				setup, found, matchedStrategy = ibsSetup, true, "CONFLUENCE"
			case ibsOK:
				setup, found, matchedStrategy = ibsSetup, true, "IBS"
			case rsi2OK:
				setup, found, matchedStrategy = rsi2Setup, true, "RSI2"
			}
			if !found {
				continue
			}

			var stats execution.IBSStats
			var riskPct float64
			switch matchedStrategy {
			case "IBS":
				s, haveStats := execution.MeasuredIBSStats[symbol]
				if !haveStats {
					continue
				}
				stats = s
				var ok bool
				riskPct, ok = execution.HalfKellyRiskPct(symbol)
				if !ok {
					continue
				}
			case "RSI2":
				s, haveStats := execution.MeasuredRSI2Stats[symbol]
				if !haveStats {
					continue
				}
				stats = s
				var ok bool
				riskPct, ok = execution.RSI2RiskPct(symbol)
				if !ok {
					continue
				}
			case "CONFLUENCE":
				s, haveStats := execution.MeasuredConfluenceStats[symbol]
				if !haveStats {
					continue
				}
				stats = s
				var ok bool
				riskPct, ok = execution.ConfluenceRiskPct(symbol)
				if !ok {
					continue
				}
				// Confluence exits on EITHER oscillator reaching its own
				// strategy's threshold, not a single generic one — see
				// currentOscillatorBps's "confluence_strength_above" case.
				setup.HoldExitRule = "confluence_strength_above"
			default:
				continue
			}
			entryPrice := spec.Price(int64(setup.Entry))
			stopDistPrice := entryPrice * execution.ProtectiveStopBps(stats.AvgLossBps) / 10000
			minDeal := instrument.MinDealSize(symbol)
			size := execution.SizeToStop(acct.Balance, riskPct, stopDistPrice, minDeal)
			if size <= 0 {
				continue
			}
			// Hard backstop independent of the margin-factor math above:
			// never commit more than 15% of equity in notional to one
			// trade, regardless of what the size calculation above
			// produced. Protects against a unit-conversion mistake in
			// MinDealSize/marginFactorPct actually causing an oversized
			// order instead of just a documentation note about one.
			if notional := size * entryPrice; notional > acct.Balance*0.15 {
				logExec(map[string]any{"event": "entry_blocked", "symbol": symbol, "reason": "notional backstop: computed size too large relative to equity", "notional": notional, "equity": acct.Balance})
				continue
			}
			thisMarginPct := size * entryPrice * marginFactor / 100 / acct.Balance * 100

			ok, reason := risk.ApproveEntry(acct.Balance, len(trades.Trades), committedMarginPct, thisMarginPct)
			if !ok {
				logExec(map[string]any{"event": "entry_blocked", "symbol": symbol, "reason": reason})
				continue
			}

			direction := "BUY"
			stopLevel := entryPrice - stopDistPrice
			if setup.Side == "sell" {
				direction = "SELL"
				stopLevel = entryPrice + stopDistPrice
			}
			epic, ok := instrument.CapitalEpic(symbol)
			if !ok {
				epic = symbol
			}
			dealID, fillPrice, err := bro.PlaceOrder(epic, direction, size, stopLevel)
			if err != nil {
				logExec(map[string]any{"event": "entry_failed", "symbol": symbol, "error": err.Error()})
				continue
			}
			if fillPrice > 0 {
				entryPrice = fillPrice
			}
			hold := t_holdBars(setup)
			trades.Trades[symbol] = openTrade{
				Symbol: symbol, DealID: dealID, Side: setup.Side, EntryPrice: entryPrice,
				Size: size, MarginPct: thisMarginPct, HoldExitRule: setup.HoldExitRule,
				HoldExitThresholdBps: setup.HoldExitThresholdBps, MaxHoldBars: hold,
				LastBarSeen: window[len(window)-1].CloseTime, OpenedAt: time.Now().UTC(),
			}
			committedMarginPct += thisMarginPct
			sig := buildStrategySignal(detectors[0], setup, spec, source.Interval(), spreadTicks, time.Now().UTC())
			delivery.Send(ctx, sig)
			logExec(map[string]any{"event": "entry", "symbol": symbol, "deal_id": dealID, "side": setup.Side,
				"entry": entryPrice, "size": size, "risk_pct": riskPct, "stop": stopLevel, "margin_pct": thisMarginPct})
		}
		saveOpenTrades(openTradesPath, trades)
	}

	scanOnce()
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			fmt.Fprintln(stdout, "tradex execute: stopped")
			return 0
		case <-ticker.C:
			scanOnce()
		}
	}
}

func t_holdBars(setup scan.Setup) int {
	if setup.HoldBars > 0 {
		return setup.HoldBars
	}
	return 10
}

func ibsOf(c domain.Candle, spec instrument.Spec) (int64, bool) {
	rng := int64(c.High - c.Low)
	if rng <= 0 {
		return 0, false
	}
	return int64(c.Close-c.Low) * 10000 / rng, true
}

// currentOscillatorBps dispatches to the right underlying reading (IBS
// needs only the latest candle; RSI2 needs the window's last 3 closes)
// based on the hold-exit rule's strategy prefix, so the same generic
// exit-management loop works for either strategy.
func currentOscillatorBps(holdExitRule string, window []domain.Candle) (int64, bool) {
	if len(window) == 0 {
		return 0, false
	}
	switch {
	case strings.HasPrefix(holdExitRule, "ibs_"):
		return ibsOf(window[len(window)-1], instrument.Spec{})
	case strings.HasPrefix(holdExitRule, "rsi2_"):
		return features.RSI2Bps(window)
	}
	return 0, false
}

func tradeableSymbols(all []string) []string {
	var out []string
	for _, s := range all {
		_, inIBS := execution.MeasuredIBSStats[s]
		_, inRSI2 := execution.MeasuredRSI2Stats[s]
		_, inConfluence := execution.MeasuredConfluenceStats[s]
		if inIBS || inRSI2 || inConfluence {
			out = append(out, s)
		}
	}
	return out
}

var _ = strings.ToLower // keep import if unused elsewhere in file set
