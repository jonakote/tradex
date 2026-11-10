// Command stratlab measures any integrated strategy over a CSV candle file,
// reporting full sample and both time halves with a verdict. It is the single
// measurement entry point for every stop/target strategy (continuation, ICT,
// AMN) and for IBS, so results are directly comparable.
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
	"tradex/internal/feed"
	"tradex/internal/instrument"
	"tradex/internal/strategy/scan"
)

func main() {
	var (
		strategy  = flag.String("strategy", "", "continuation | ict | amn | structuretrend | trianglebreakout | ibs (required)")
		symbol    = flag.String("symbol", "", "instrument symbol (required)")
		path      = flag.String("candles", "", "CSV candle file (required)")
		tf        = flag.String("tf", "1h", "candle interval")
		scaleFlag = flag.Int64("scale", 0, "price scale; 0 resolves from internal/instrument")
		spread    = flag.Int64("spread-ticks", 0, "spread charged per trade")
		slippage  = flag.Int64("slippage-ticks", 2, "slippage each side")
		expiry    = flag.Int("expiry-bars", 8, "unfilled limit lifetime")
		maxHold   = flag.Int("max-hold-bars", 48, "time-stop for a stalled position")
		riskUnit  = flag.Int64("risk-unit-ticks", 100, "R unit for hold-exit strategies (IBS)")
		ibsHold   = flag.Int("ibs-hold", 1, "IBS max hold bars")
		ibsLong   = flag.Int64("ibs-long-below-bps", 2000, "IBS long threshold")
		ibsShort  = flag.Int64("ibs-short-above-bps", 0, "IBS short threshold")
		ibsTrend  = flag.Int("ibs-trend-filter-bars", 0, "IBS SMA trend filter window (0 disables)")
		ibsExit   = flag.Int64("ibs-strength-exit-bps", 0, "IBS exit-on-strength threshold (0 disables, uses fixed hold)")
	)
	flag.Parse()
	if *strategy == "" || *symbol == "" || *path == "" {
		fmt.Fprintln(os.Stderr, "stratlab: -strategy, -symbol and -candles are required")
		os.Exit(2)
	}
	spec, ok := instrument.Lookup(*symbol)
	scaleVal := *scaleFlag
	if ok && scaleVal == 0 {
		scaleVal = spec.PriceScale
	}
	if scaleVal == 0 {
		fmt.Fprintf(os.Stderr, "stratlab: %s has no verified precision; pass -scale\n", *symbol)
		os.Exit(1)
	}
	interval := feed.IntervalDuration(*tf)
	if interval <= 0 {
		fmt.Fprintf(os.Stderr, "stratlab: unknown timeframe %q\n", *tf)
		os.Exit(1)
	}

	det, err := buildDetector(*strategy, *ibsHold, *ibsLong, *ibsShort, *ibsTrend, *ibsExit)
	if err != nil {
		fmt.Fprintf(os.Stderr, "stratlab: %v\n", err)
		os.Exit(1)
	}

	candles, err := readCandles(*path, scaleVal, instrument.Normalize(*symbol), interval)
	if err != nil {
		fmt.Fprintf(os.Stderr, "stratlab: %v\n", err)
		os.Exit(1)
	}

	params := scan.DefaultBTParams()
	params.Costs = scan.Costs{SpreadTicks: *spread, SlippageTicks: *slippage}
	params.SpreadTicks = *spread
	params.ExpiryBars = *expiry
	params.MaxHoldBars = *maxHold
	params.RiskUnitTicks = *riskUnit

	run := func(c []domain.Candle) (scan.BTResult, error) {
		r, _, err := scan.Backtest(instrument.Normalize(*symbol), spec, interval, c, det, params)
		return r, err
	}
	full, err := run(candles)
	if err != nil {
		fmt.Fprintf(os.Stderr, "stratlab: %v\n", err)
		os.Exit(1)
	}
	mid := len(candles) / 2
	firstHalf, errA := run(candles[:mid])
	secondHalf, errB := run(candles[mid:])

	fmt.Printf("stratlab  %s  %s  %s  %s\n", det.Name(), det.Spec(), full.Symbol, *tf)
	fmt.Printf("candles   %d bars  %s .. %s   costs spread %d + 2x slip %d\n\n",
		len(candles), full.From.Format("2006-01-02"), full.To.Format("2006-01-02"), *spread, *slippage)
	hdr := fmt.Sprintf("%-12s %7s %7s %7s %12s %7s %8s", "sample", "armed", "filled", "trades", "expectancy", "win%", "maxDD")
	fmt.Println(hdr)
	fmt.Println(strings.Repeat("-", len(hdr)))
	row("full", full, nil)
	row("first half", firstHalf, errA)
	row("second half", secondHalf, errB)

	fmt.Printf("\nexit reasons  stop=%d target=%d time=%d (ambiguous stop=%d)\n", full.StopExits, full.TargetExits, full.TimeExits, full.AmbiguousStops)
	fmt.Println()
	t := full.Report.Trades
	switch {
	case t < 30:
		fmt.Printf("VERDICT: not measurable — %d trades.\n", t)
	case full.ExpectancyRBps <= 0:
		fmt.Printf("VERDICT: negative. %+.3f R/trade over %d trades. Do not promote.\n", rr(full.ExpectancyRBps), t)
	case errA == nil && errB == nil && (firstHalf.ExpectancyRBps <= 0 || secondHalf.ExpectancyRBps <= 0):
		fmt.Printf("VERDICT: not robust. Full %+.3f R, halves %+.3f / %+.3f R.\n",
			rr(full.ExpectancyRBps), rr(firstHalf.ExpectancyRBps), rr(secondHalf.ExpectancyRBps))
	default:
		fmt.Printf("VERDICT: positive in both halves (%+.3f / %+.3f R). Confirm on another instrument.\n",
			rr(firstHalf.ExpectancyRBps), rr(secondHalf.ExpectancyRBps))
	}
}

func rr(bps int64) float64 { return float64(bps) / 10000 }

func row(label string, r scan.BTResult, err error) {
	if err != nil {
		fmt.Printf("%-12s %s\n", label, err)
		return
	}
	win := 0.0
	if r.Report.Trades > 0 {
		win = float64(r.Wins) * 100 / float64(r.Report.Trades)
	}
	fmt.Printf("%-12s %7d %7d %7d %+11.3fR %6.1f%% %7.1fR\n",
		label, r.Armed, r.Filled, r.Report.Trades, rr(r.ExpectancyRBps), win, rr(r.MaxDrawdownRBps))
}

func buildDetector(name string, ibsHold int, ibsLong, ibsShort int64, ibsTrend int, ibsExit int64) (scan.Detector, error) {
	switch strings.ToLower(name) {
	case "continuation":
		return scan.Continuation{Config: features.DefaultContinuationConfig()}, nil
	case "ict":
		return scan.ICT{Config: features.DefaultICTConfig()}, nil
	case "amn":
		return scan.AMN{Config: features.DefaultAMNConfig()}, nil
	case "structuretrend":
		return scan.StructureTrend{Config: features.DefaultStructureTrendConfig()}, nil
	case "trianglebreakout":
		return scan.TriangleBreakout{Config: features.DefaultTriangleBreakoutConfig()}, nil
	case "ibs":
		return scan.IBS{HoldBars: ibsHold, LongBelowBps: ibsLong, ShortAboveBps: ibsShort,
			TrendFilterBars: ibsTrend, StrengthExitBps: ibsExit}, nil
	}
	return nil, fmt.Errorf("unknown strategy %q", name)
}

func readCandles(path string, scaleVal int64, symbol string, tf time.Duration) ([]domain.Candle, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	reader := csv.NewReader(file)
	reader.FieldsPerRecord = -1
	var out []domain.Candle
	line := 0
	for {
		rec, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		line++
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
				return nil, fmt.Errorf("line %d bad price %q", line, rec[i+1])
			}
			s := v * float64(scaleVal)
			if s >= 0 {
				p[i] = domain.Price(int64(s + 0.5))
			} else {
				p[i] = domain.Price(int64(s - 0.5))
			}
		}
		ot := time.UnixMilli(ms).UTC()
		c := domain.Candle{Source: "csv", Symbol: symbol, Timeframe: tf,
			OpenTime: ot, CloseTime: ot.Add(tf), Open: p[0], High: p[1], Low: p[2], Close: p[3], TickCount: 1}
		if c.Validate() == nil {
			out = append(out, c)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s contained no valid candles", path)
	}
	return out, nil
}
