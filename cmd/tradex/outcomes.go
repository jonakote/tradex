package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"tradex/internal/domain"
	"tradex/internal/feed"
	"tradex/internal/instrument"
)

// journalRecord mirrors notify.Signal's JSON shape (internal/notify/notify.go)
// as written to the forward-test journal files by `tradex watch`.
type journalRecord struct {
	Validated   bool           `json:"validated"`
	Strategy    string         `json:"strategy"`
	SpecVersion string         `json:"spec_version"`
	Symbol      string         `json:"symbol"`
	Side        string         `json:"side"`
	Timeframe   string         `json:"timeframe"`
	EntryType   string         `json:"entry_type"`
	Entry       string         `json:"entry"`
	Stop        string         `json:"stop"`
	Target      string         `json:"target"`
	DetectedAt  time.Time      `json:"detected_at"`
	EmittedAt   time.Time      `json:"emitted_at"`
	Diagnostics map[string]any `json:"diagnostics,omitempty"`
}

// outcomeStatus is the resolved state of a forward-test signal, walked
// forward against the venue's own subsequent candles.
type outcomeStatus string

const (
	statusWin      outcomeStatus = "WIN"
	statusLoss     outcomeStatus = "LOSS"
	statusOpen     outcomeStatus = "OPEN"
	statusPending  outcomeStatus = "PENDING" // limit never filled yet
	statusTimeExit outcomeStatus = "TIME_EXIT"
	statusSkipped  outcomeStatus = "SKIPPED" // self-test or unparseable row
)

type outcome struct {
	Strategy   string        `json:"strategy"`
	SpecVer    string        `json:"spec_version,omitempty"`
	Symbol     string        `json:"symbol"`
	Timeframe  string        `json:"timeframe"`
	Side       string        `json:"side"`
	EntryType  string        `json:"entry_type"`
	Entry      float64       `json:"entry"`
	Stop       float64       `json:"stop,omitempty"`
	Target     float64       `json:"target,omitempty"`
	DetectedAt time.Time     `json:"detected_at"`
	Status     outcomeStatus `json:"status"`
	FilledAt   time.Time     `json:"filled_at,omitempty"`
	ExitAt     time.Time     `json:"exit_at,omitempty"`
	ExitPrice  float64       `json:"exit_price,omitempty"`
	PnLTicks   int64         `json:"pnl_ticks,omitempty"`
	PnLR       float64       `json:"pnl_r,omitempty"` // multiple of planned risk, when stop/target defined
	Note       string        `json:"note,omitempty"`
}

// runOutcomes resolves every signal in one or more journal files against
// live venue candles: did the limit fill, did it hit stop or target first,
// or is it still open/pending. This is read-only analysis; it never places
// orders and never mutates the journal files.
func runOutcomes(args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		fmt.Fprintln(stderr, "usage: tradex outcomes <journal.jsonl> [journal2.jsonl ...]")
		return 2
	}

	var records []journalRecord
	for _, path := range args {
		rows, err := readJournal(path)
		if err != nil {
			fmt.Fprintf(stderr, "tradex outcomes: %s: %v\n", path, err)
			return 1
		}
		records = append(records, rows...)
	}

	// Group by (symbol, timeframe) so each pair is fetched from the venue
	// exactly once, from the earliest detection to now.
	type key struct{ symbol, timeframe string }
	groups := map[key][]journalRecord{}
	for _, r := range records {
		if strings.Contains(strings.ToUpper(r.Strategy), "SELF-TEST") {
			continue
		}
		if r.Entry == "" || r.Entry == "0.00000" {
			continue
		}
		groups[key{r.Symbol, r.Timeframe}] = append(groups[key{r.Symbol, r.Timeframe}], r)
	}

	now := time.Now().UTC()
	var outcomes []outcome
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	// Group further by interval alone so each interval needs only one
	// Capital.com session (login is what gets rate-limited at 429, not the
	// per-symbol history fetch), then fetch each symbol's candles under it.
	byInterval := map[string]map[string][]journalRecord{} // interval -> symbol -> rows
	skipSpec := map[string][]journalRecord{}              // symbol with no instrument.Lookup
	for k, rows := range groups {
		if _, ok := instrument.Lookup(k.symbol); !ok {
			skipSpec[k.symbol] = append(skipSpec[k.symbol], rows...)
			continue
		}
		interval := normalizeInterval(k.timeframe)
		if byInterval[interval] == nil {
			byInterval[interval] = map[string][]journalRecord{}
		}
		byInterval[interval][k.symbol] = append(byInterval[interval][k.symbol], rows...)
	}
	for symbol, rows := range skipSpec {
		for _, r := range rows {
			outcomes = append(outcomes, outcome{Strategy: r.Strategy, Symbol: symbol, Timeframe: r.Timeframe,
				Status: statusSkipped, Note: "no verified instrument precision"})
		}
	}

	intervals := make([]string, 0, len(byInterval))
	for iv := range byInterval {
		intervals = append(intervals, iv)
	}
	sort.Strings(intervals)

	for _, interval := range intervals {
		bySymbol := byInterval[interval]
		symbols := make([]string, 0, len(bySymbol))
		for s := range bySymbol {
			symbols = append(symbols, s)
		}
		sort.Strings(symbols)

		source, err := feed.NewCapital(feed.CapitalConfig{
			APIKey:     os.Getenv("CAPITAL_API_KEY"),
			Identifier: os.Getenv("CAPITAL_IDENTIFIER"),
			Password:   os.Getenv("CAPITAL_PASSWORD"),
			BaseURL:    os.Getenv("CAPITAL_BASE_URL"),
			Symbols:    symbols,
			Interval:   interval,
			Side:       "bid",
		})
		if err != nil {
			for _, symbol := range symbols {
				for _, r := range bySymbol[symbol] {
					outcomes = append(outcomes, outcome{Strategy: r.Strategy, Symbol: symbol, Timeframe: r.Timeframe,
						Entry: mustFloat(r.Entry), DetectedAt: r.DetectedAt, Status: statusSkipped,
						Note: fmt.Sprintf("feed init failed: %v", err)})
				}
			}
			continue
		}

		for i, symbol := range symbols {
			rows := bySymbol[symbol]
			spec, _ := instrument.Lookup(symbol)
			earliest := rows[0].DetectedAt
			for _, r := range rows {
				if r.DetectedAt.Before(earliest) {
					earliest = r.DetectedAt
				}
			}
			var candles []domain.Candle
			var fetchErr error
			for attempt := 0; attempt < 4; attempt++ {
				candles, fetchErr = source.History(ctx, symbol, earliest.Add(-time.Hour), now)
				if fetchErr == nil || !strings.Contains(fetchErr.Error(), "429") {
					break
				}
				time.Sleep(time.Duration(3+attempt*5) * time.Second) // backoff on rate limit
			}
			if fetchErr != nil {
				for _, r := range rows {
					outcomes = append(outcomes, outcome{Strategy: r.Strategy, Symbol: symbol, Timeframe: r.Timeframe,
						Entry: mustFloat(r.Entry), DetectedAt: r.DetectedAt, Status: statusSkipped,
						Note: fmt.Sprintf("candle fetch failed: %v", fetchErr)})
				}
				continue
			}
			for _, r := range rows {
				outcomes = append(outcomes, resolveSignal(r, candles, spec))
			}
			if i < len(symbols)-1 {
				time.Sleep(2 * time.Second) // be gentle between symbols on the same session
			}
		}
	}

	sort.Slice(outcomes, func(i, j int) bool { return outcomes[i].DetectedAt.Before(outcomes[j].DetectedAt) })

	// Human-readable report.
	wins, losses, open, pending, timeExits := 0, 0, 0, 0, 0
	for _, o := range outcomes {
		fmt.Fprintf(stdout, "%-6s %-14s %-8s %-4s entry %-10.5f %-7s  %s",
			o.Timeframe, o.Strategy, o.Symbol, strings.ToUpper(o.Side), o.Entry, string(o.Status),
			o.DetectedAt.Format("2006-01-02 15:04"))
		switch o.Status {
		case statusWin, statusLoss:
			fmt.Fprintf(stdout, "  exit %.5f @ %s  (%.2fR)", o.ExitPrice, o.ExitAt.Format("01-02 15:04"), o.PnLR)
		case statusOpen:
			fmt.Fprintf(stdout, "  %s", o.Note)
		case statusPending:
			fmt.Fprintf(stdout, "  %s", o.Note)
		case statusTimeExit:
			fmt.Fprintf(stdout, "  exit %.5f @ %s", o.ExitPrice, o.ExitAt.Format("01-02 15:04"))
		case statusSkipped:
			fmt.Fprintf(stdout, "  %s", o.Note)
		}
		fmt.Fprintln(stdout)
		switch o.Status {
		case statusWin:
			wins++
		case statusLoss:
			losses++
		case statusOpen:
			open++
		case statusPending:
			pending++
		case statusTimeExit:
			timeExits++
		}
	}
	resolved := wins + losses
	fmt.Fprintln(stdout, strings.Repeat("-", 60))
	fmt.Fprintf(stdout, "resolved: %d win / %d loss", wins, losses)
	if resolved > 0 {
		fmt.Fprintf(stdout, " (%.0f%% win rate)", float64(wins)*100/float64(resolved))
	}
	fmt.Fprintf(stdout, " | open: %d | pending (unfilled): %d | time-exit: %d\n", open, pending, timeExits)

	return 0
}

func readJournal(path string) ([]journalRecord, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var rows []journalRecord
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var rec journalRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue // tolerate stray/corrupt lines rather than aborting the whole report
		}
		rows = append(rows, rec)
	}
	return rows, scanner.Err()
}

func mustFloat(s string) float64 {
	s = strings.TrimSpace(s)
	if s == "" || s == "—" {
		return 0
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return v
}

func normalizeInterval(tf string) string {
	switch strings.ToLower(tf) {
	case "1d", "d", "day", "daily":
		return "1d"
	default:
		return "1h"
	}
}

// resolveSignal walks the candle series forward from detection: first to
// see whether a LIMIT order ever fills, then whether stop or target is hit
// first (same-bar ties are scored conservatively as the stop), or for
// hold-bar time-exit signals (Stop==Target==0) whether the hold period has
// elapsed yet.
func resolveSignal(r journalRecord, candles []domain.Candle, spec instrument.Spec) outcome {
	side := strings.ToLower(r.Side)
	entry := mustFloat(r.Entry)
	stop := mustFloat(r.Stop)
	target := mustFloat(r.Target)
	holdBars := 0
	if hb, ok := r.Diagnostics["hold_bars"]; ok {
		if f, ok := hb.(float64); ok {
			holdBars = int(f)
		}
	}

	out := outcome{
		Strategy: r.Strategy, SpecVer: r.SpecVersion, Symbol: r.Symbol, Timeframe: r.Timeframe,
		Side: side, EntryType: r.EntryType, Entry: entry, Stop: stop, Target: target, DetectedAt: r.DetectedAt,
	}

	price := func(p domain.Price) float64 { return spec.Price(int64(p)) }

	// Only candles strictly after detection matter for fill/exit simulation.
	var after []domain.Candle
	for _, c := range candles {
		if c.OpenTime.After(r.DetectedAt) || c.OpenTime.Equal(r.DetectedAt) {
			after = append(after, c)
		}
	}

	// --- Time-exit (IBS-style): no stop/target, filled at detection, exits
	// after holdBars subsequent closed bars.
	if stop == 0 && target == 0 && holdBars > 0 {
		out.FilledAt = r.DetectedAt
		if len(after) < holdBars {
			out.Status = statusOpen
			if len(after) > 0 {
				last := after[len(after)-1]
				cur := price(last.Close)
				ticks := int64((cur - entry) * float64(spec.PriceScale))
				if side == "sell" {
					ticks = -ticks
				}
				out.PnLTicks = ticks
				out.Note = fmt.Sprintf("day %d/%d, floating %+.1f pips", len(after), holdBars,
					float64(ticks)/pipDivisor(spec))
			} else {
				out.Note = fmt.Sprintf("day 0/%d, no bars yet", holdBars)
			}
			return out
		}
		exitBar := after[holdBars-1]
		exitPrice := price(exitBar.Close)
		out.Status = statusTimeExit
		out.ExitAt = exitBar.CloseTime
		out.ExitPrice = exitPrice
		ticks := int64((exitPrice - entry) * float64(spec.PriceScale))
		if side == "sell" {
			ticks = -ticks
		}
		out.PnLTicks = ticks
		out.Note = fmt.Sprintf("%+.1f pips over %d bars", float64(ticks)/pipDivisor(spec), holdBars)
		return out
	}

	// --- Stop/target signals: MARKET fills immediately; LIMIT fills when a
	// subsequent bar's range crosses the entry price.
	fillIdx := -1
	if strings.EqualFold(r.EntryType, "MARKET") {
		if len(after) == 0 {
			out.Status = statusPending
			out.Note = "no bars since detection yet"
			return out
		}
		fillIdx = 0
		out.FilledAt = r.DetectedAt
	} else {
		for i, c := range after {
			lo, hi := price(c.Low), price(c.High)
			if side == "buy" && lo <= entry {
				fillIdx = i
				out.FilledAt = c.OpenTime
				break
			}
			if side == "sell" && hi >= entry {
				fillIdx = i
				out.FilledAt = c.OpenTime
				break
			}
		}
		if fillIdx == -1 {
			out.Status = statusPending
			if len(after) > 0 {
				cur := price(after[len(after)-1].Close)
				dist := (cur - entry) * float64(spec.PriceScale) / pipDivisor(spec)
				if side == "sell" {
					dist = -dist
				}
				out.Note = fmt.Sprintf("unfilled %d bars, price %.1f pips away from entry", len(after), dist)
			} else {
				out.Note = "unfilled, no bars since detection yet"
			}
			return out
		}
	}

	// Walk forward from the fill bar (inclusive, since a LIMIT fill bar's
	// remaining range can still reach stop/target the same bar) looking for
	// whichever of stop/target is touched first. A same-bar touch of both is
	// scored as the stop — conservative, since we don't know intrabar order.
	for i := fillIdx; i < len(after); i++ {
		c := after[i]
		lo, hi := price(c.Low), price(c.High)
		hitStop, hitTarget := false, false
		if stop != 0 {
			if side == "buy" && lo <= stop {
				hitStop = true
			}
			if side == "sell" && hi >= stop {
				hitStop = true
			}
		}
		if target != 0 {
			if side == "buy" && hi >= target {
				hitTarget = true
			}
			if side == "sell" && lo <= target {
				hitTarget = true
			}
		}
		if hitStop {
			out.Status = statusLoss
			out.ExitAt = c.CloseTime
			out.ExitPrice = stop
			ticks := int64((stop - entry) * float64(spec.PriceScale))
			if side == "sell" {
				ticks = -ticks
			}
			out.PnLTicks = ticks
			out.PnLR = riskMultiple(entry, stop, target, side, stop)
			return out
		}
		if hitTarget {
			out.Status = statusWin
			out.ExitAt = c.CloseTime
			out.ExitPrice = target
			ticks := int64((target - entry) * float64(spec.PriceScale))
			if side == "sell" {
				ticks = -ticks
			}
			out.PnLTicks = ticks
			out.PnLR = riskMultiple(entry, stop, target, side, target)
			return out
		}
	}

	// Filled, neither stop nor target hit yet: open position, report floating.
	out.Status = statusOpen
	last := after[len(after)-1]
	cur := price(last.Close)
	ticks := int64((cur - entry) * float64(spec.PriceScale))
	if side == "sell" {
		ticks = -ticks
	}
	out.PnLTicks = ticks
	out.PnLR = riskMultiple(entry, stop, target, side, cur)
	out.Note = fmt.Sprintf("floating %+.1f pips (%.2fR)", float64(ticks)/pipDivisor(spec), out.PnLR)
	return out
}

func pipDivisor(spec instrument.Spec) float64 {
	// A pip is PipSize price units; TickSize is 1/PriceScale price units
	// per tick, so a pip is PipSize*PriceScale ticks. Instruments with no
	// pip convention (PipSize == 0, e.g. crypto) fall back to 1 tick.
	if !spec.HasPipConvention() {
		return 1
	}
	return spec.PipSize * float64(spec.PriceScale)
}

func riskMultiple(entry, stop, target float64, side string, at float64) float64 {
	if stop == 0 {
		return 0
	}
	risk := entry - stop
	if side == "sell" {
		risk = stop - entry
	}
	if risk == 0 {
		return 0
	}
	move := at - entry
	if side == "sell" {
		move = entry - at
	}
	return move / risk
}
