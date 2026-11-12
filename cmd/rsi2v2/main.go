// Command rsi2v2 measures Connors' RSI(2) mean-reversion signal with the
// same v0.2 overlay as IBS (200-SMA trend filter, exit-on-strength, capped
// hold) so the two are directly comparable. Mirrors cmd/ibsv2 structure
// exactly on purpose -- same baseline-drift accounting, same cost/swap
// conventions, same split-date holdout check.
//
// Parameters are the published Connors RSI(2) thresholds (<=10 oversold,
// >=90 overbought by convention; this project measured <=10/>=70 and
// >=90/<=30 is also worth trying) -- not fitted to any result here.
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
	longBelow := flag.Int64("long-below-bps", 1000, "long when RSI(2) <= this (1000 = 10)")
	shortAbove := flag.Int64("short-above-bps", 9000, "short when RSI(2) >= this (9000 = 90)")
	exitAbove := flag.Int64("exit-above-bps", 7000, "exit-on-strength: close out a long when a later bar's RSI(2) >= this")
	exitBelow := flag.Int64("exit-below-bps", 3000, "exit-on-weakness: close out a short when a later bar's RSI(2) <= this")
	side := flag.String("side", "long", "long | short | both")
	maxHold := flag.Int("max-hold-bars", 10, "fallback max hold if strength exit never fires")
	costBps := flag.Int64("cost-bps", 0, "round-trip cost in bps of price (0 = use per-symbol default)")
	longSwapBps := flag.Float64("long-swap-bps", 0, "overnight financing cost per day held for a LONG, in bps of price (positive = cost, negative = credit)")
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
		retBps    int64
		holdBars  int
		exitKind  string
		side      string
	}
	var trades []trade

	takeLong := *side == "long" || *side == "both"
	takeShort := *side == "short" || *side == "both"

	for i := *trendBars; i < len(candles)-1; i++ {
		if i < 2 {
			continue
		}
		sma := sma(candles[i-*trendBars+1 : i+1])
		c := candles[i]
		rsi, ok := features.RSI2Bps(candles[i-2 : i+1])
		if !ok {
			continue
		}
		up := int64(c.Close) > sma
		down := !up

		tryLong := takeLong && up && rsi <= *longBelow
		tryShort := takeShort && down && rsi >= *shortAbove
		if !tryLong && !tryShort {
			continue
		}
		entry := c.Close
		exitIdx := -1
		kind := "MAX_HOLD"
		for h := 1; h <= *maxHold && i+h < len(candles); h++ {
			if i+h < 2 {
				continue
			}
			fRSI, ok := features.RSI2Bps(candles[i+h-2 : i+h+1])
			if !ok {
				continue
			}
			if tryLong && fRSI >= *exitAbove {
				exitIdx = i + h
				kind = "STRENGTH"
				break
			}
			if tryShort && fRSI <= *exitBelow {
				exitIdx = i + h
				kind = "STRENGTH"
				break
			}
		}
		if exitIdx < 0 {
			h := *maxHold
			if i+h >= len(candles) {
				continue
			}
			exitIdx = i + h
		}
		exit := candles[exitIdx]
		holdBars := exitIdx - i
		var grossBps int64
		var swapBps float64
		sideLabel := "long"
		if tryLong {
			grossBps = int64(exit.Close-entry) * 10000 / int64(entry)
			swapBps = *longSwapBps * float64(holdBars)
		} else {
			sideLabel = "short"
			grossBps = int64(entry-exit.Close) * 10000 / int64(entry)
		}
		netBps := grossBps - cost - int64(swapBps+0.5)

		trades = append(trades, trade{closeTime: exit.CloseTime, retBps: netBps, holdBars: holdBars, exitKind: kind, side: sideLabel})
	}

	report := func(label string, ts []trade) {
		if len(ts) == 0 {
			fmt.Printf("%-14s 0 trades\n", label)
			return
		}
		var sumRet int64
		var sumDrift float64
		var sumWin, sumLoss int64
		wins, strengthExits := 0, 0
		var sumHold int
		for _, t := range ts {
			sumRet += t.retBps
			sumDrift += baseline[t.holdBars]
			sumHold += t.holdBars
			if t.retBps > 0 {
				wins++
				sumWin += t.retBps
			} else if t.retBps < 0 {
				sumLoss += -t.retBps
			}
			if t.exitKind == "STRENGTH" {
				strengthExits++
			}
		}
		n := float64(len(ts))
		meanDriftBps := sumDrift / n
		edge := float64(sumRet)/n - meanDriftBps
		losses := len(ts) - wins
		avgWin, avgLoss := 0.0, 0.0
		if wins > 0 {
			avgWin = float64(sumWin) / float64(wins)
		}
		if losses > 0 {
			avgLoss = float64(sumLoss) / float64(losses)
		}
		fmt.Printf("%-14s %4d trades  mean_ret %+7.2fbp  drift %+7.2fbp  edge %+7.2fbp  win%% %5.1f%%  avg_hold %.1fbars  strength_exit%% %.1f%%  avg_win %+6.2fbp  avg_loss %6.2fbp\n",
			label, len(ts), float64(sumRet)/n, meanDriftBps, edge, 100*float64(wins)/n, float64(sumHold)/n, 100*float64(strengthExits)/n, avgWin, avgLoss)
	}

	var pre, post []trade
	for _, t := range trades {
		if t.closeTime.Before(split) {
			pre = append(pre, t)
		} else {
			post = append(post, t)
		}
	}
	fmt.Printf("symbol=%s trend_bars=%d long_below=%dbps short_above=%dbps exit_above=%dbps exit_below=%dbps side=%s max_hold=%d cost=%dbps split=%s\n",
		*symbol, *trendBars, *longBelow, *shortAbove, *exitAbove, *exitBelow, *side, *maxHold, cost, *splitDate)
	report("full", trades)
	report("pre-split", pre)
	report("post-split", post)
}

func defaultCostBps(symbol string) int64 {
	switch {
	case strings.Contains(symbol, "USDT"):
		return 20
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
				continue
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
