// Command ibsv2 measures IBS mean-reversion v0.2: a 200-SMA trend filter
// (long-only in an uptrend, matching the literature's required overlay) and
// exit-on-strength (exit when a later bar's own IBS crosses back toward the
// opposite extreme, capped by a max hold), instead of the raw signal's fixed
// 1-bar hold. Parameters are stated priors from the published Connors
// RSI-2 methodology and this project's own MEANREVERSION_SPEC.md "what would
// make this promotable" list — not fitted to any result. Computes win rate,
// mean return, and the signal-vs-drift edge (forward return minus the
// all-bar baseline) so a result isn't credited to drift.
//
// Not wired into any build target; promote into scan.Detector (extending
// Setup/Stream for a signal-exit, not just HoldBars/stop-target) only if a
// genuine holdout confirms an edge.
package main

import (
	"encoding/csv"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"tradex/internal/domain"
	"tradex/internal/features"
	"tradex/internal/instrument"
)

func main() {
	symbol := flag.String("symbol", "", "instrument symbol")
	path := flag.String("candles", "", "CSV candle file (open_time_ms,open,high,low,close,volume)")
	trendBars := flag.Int("trend-bars", 200, "SMA window for the trend filter")
	ibsLongBelow := flag.Int64("ibs-long-below-bps", 2000, "long when IBS <= this (2000 = 0.20)")
	ibsExitAbove := flag.Int64("ibs-exit-above-bps", 5000, "exit-on-strength: close out when a later bar's IBS >= this (5000 = 0.50)")
	maxHold := flag.Int("max-hold-bars", 10, "fallback max hold if strength exit never fires")
	costBps := flag.Int64("cost-bps", 0, "round-trip cost in bps of price (0 = use per-symbol default)")
	splitDate := flag.String("split-date", "2023-01-01", "pre/post split for the holdout check")
	flag.Parse()

	spec, ok := instrument.Lookup(*symbol)
	if !ok {
		fmt.Fprintln(os.Stderr, "unverified symbol")
		os.Exit(1)
	}
	candles, err := readCandles(*path, spec.PriceScale, instrument.Normalize(*symbol))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	cost := *costBps
	if cost == 0 {
		cost = defaultCostBps(*symbol)
	}
	split, err := time.Parse("2006-01-02", *splitDate)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	// Unconditional baseline: for each holding length h (1..maxHold), the
	// mean forward return over h bars starting from EVERY bar in the
	// series (not conditioned on the signal), so a trade's edge compares
	// against the baseline for the SAME holding period, not its own future.
	baseline := make([]float64, *maxHold+1)
	for h := 1; h <= *maxHold; h++ {
		var sum float64
		var n int
		for i := 0; i+h < len(candles); i++ {
			sum += float64(candles[i+h].Close-candles[i].Close) * 10000 / float64(candles[i].Close)
			n++
		}
		if n > 0 {
			baseline[h] = sum / float64(n)
		}
	}

	type trade struct {
		closeTime time.Time
		retBps    int64 // net of cost
		holdBars  int
		exitKind  string
	}
	var trades []trade

	for i := *trendBars; i < len(candles)-1; i++ {
		sma := sma(candles[i-*trendBars+1 : i+1])
		c := candles[i]
		ibs, ok := features.IBSBps(c)
		if !ok {
			continue
		}
		up := int64(c.Close) > sma
		if !up || ibs > *ibsLongBelow {
			continue // long-only per the trend filter; no signal
		}
		entry := c.Close
		exitIdx := -1
		kind := "MAX_HOLD"
		for h := 1; h <= *maxHold && i+h < len(candles); h++ {
			future := candles[i+h]
			fIBS, ok := features.IBSBps(future)
			if ok && fIBS >= *ibsExitAbove {
				exitIdx = i + h
				kind = "STRENGTH"
				break
			}
		}
		if exitIdx < 0 {
			h := *maxHold
			if i+h >= len(candles) {
				continue // ran off the end; drop, no fabricated exit
			}
			exitIdx = i + h
		}
		exit := candles[exitIdx]
		grossBps := int64(exit.Close-entry) * 10000 / int64(entry)
		netBps := grossBps - cost

		trades = append(trades, trade{
			closeTime: exit.CloseTime, retBps: netBps,
			holdBars: exitIdx - i, exitKind: kind,
		})
	}

	report := func(label string, ts []trade) {
		if len(ts) == 0 {
			fmt.Printf("%-14s 0 trades\n", label)
			return
		}
		var sumRet int64
		var sumDrift float64
		wins, strengthExits := 0, 0
		var sumHold int
		for _, t := range ts {
			sumRet += t.retBps
			sumDrift += baseline[t.holdBars]
			sumHold += t.holdBars
			if t.retBps > 0 {
				wins++
			}
			if t.exitKind == "STRENGTH" {
				strengthExits++
			}
		}
		n := float64(len(ts))
		meanDriftBps := sumDrift / n
		edge := float64(sumRet)/n - meanDriftBps
		fmt.Printf("%-14s %4d trades  mean_ret %+7.2fbp  drift %+7.2fbp  edge %+7.2fbp  win%% %5.1f%%  avg_hold %.1fbars  strength_exit%% %.1f%%\n",
			label, len(ts), float64(sumRet)/n, meanDriftBps, edge, 100*float64(wins)/n, float64(sumHold)/n, 100*float64(strengthExits)/n)
	}

	var pre, post []trade
	for _, t := range trades {
		if t.closeTime.Before(split) {
			pre = append(pre, t)
		} else {
			post = append(post, t)
		}
	}
	fmt.Printf("symbol=%s trend_bars=%d ibs_long_below=%dbps ibs_exit_above=%dbps max_hold=%d cost=%dbps split=%s\n",
		*symbol, *trendBars, *ibsLongBelow, *ibsExitAbove, *maxHold, cost, *splitDate)
	report("full", trades)
	report("pre-split", pre)
	report("post-split", post)
}

func defaultCostBps(symbol string) int64 {
	switch {
	case strings.Contains(symbol, "USDT"):
		return 20 // 0.1%/side round trip at the median-price fee convention used elsewhere
	case symbol == "XAUUSD" || symbol == "XAGUSD":
		return 5
	default:
		return 2
	}
}

func sma(bars []domain.Candle) int64 {
	var total int64
	for _, b := range bars {
		total += int64(b.Close)
	}
	return total / int64(len(bars))
}

func readCandles(path string, scaleVal int64, symbol string) ([]domain.Candle, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	reader := csv.NewReader(file)
	reader.FieldsPerRecord = -1
	var out []domain.Candle
	first := true
	for {
		rec, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if first {
			first = false
			if len(rec) > 0 && !isNumeric(rec[0]) {
				continue // header row
			}
		}
		if len(rec) < 5 {
			continue
		}
		ms, err := strconv.ParseInt(strings.TrimSpace(rec[0]), 10, 64)
		if err != nil {
			continue
		}
		p := make([]domain.Price, 4)
		for i := 0; i < 4; i++ {
			v, err := strconv.ParseFloat(strings.TrimSpace(rec[i+1]), 64)
			if err != nil {
				return nil, err
			}
			s := v * float64(scaleVal)
			if s >= 0 {
				p[i] = domain.Price(int64(s + 0.5))
			} else {
				p[i] = domain.Price(int64(s - 0.5))
			}
		}
		ot := time.UnixMilli(ms).UTC()
		dur := 24 * time.Hour
		c := domain.Candle{Source: "csv", Symbol: symbol, Timeframe: dur,
			OpenTime: ot, CloseTime: ot.Add(dur), Open: p[0], High: p[1], Low: p[2], Close: p[3], TickCount: 1}
		if c.Validate() == nil {
			out = append(out, c)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s contained no valid candles", path)
	}
	return out, nil
}

func isNumeric(s string) bool {
	_, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return err == nil
}
