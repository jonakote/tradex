// Command mtflab measures whether gating a lower-timeframe (LTF) entry
// detector on a higher-timeframe (HTF) trend changes its expectancy,
// side-by-side with the unfiltered baseline, over the same candle series
// and costs stratlab already reports with. It answers one question only:
// does the HTF filter help, on this sample, in both halves — never applied
// to live trading without that confirmation.
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
		strategy   = flag.String("strategy", "", "continuation | ict | amn (required)")
		symbol     = flag.String("symbol", "", "instrument symbol (required)")
		ltfPath    = flag.String("ltf-candles", "", "LTF entry-timeframe CSV (required)")
		htfPath    = flag.String("htf-candles", "", "HTF bias-timeframe CSV (required)")
		ltf        = flag.String("ltf", "15m", "LTF interval")
		htf        = flag.String("htf", "1h", "HTF interval")
		spread     = flag.Int64("spread-ticks", 10, "spread charged per trade")
		slippage   = flag.Int64("slippage-ticks", 2, "slippage each side")
		expiry     = flag.Int("expiry-bars", 8, "unfilled limit lifetime")
		maxHold    = flag.Int("max-hold-bars", 48, "time-stop for a stalled position")
		swingLeft  = flag.Int("htf-swing-left", 3, "HTF swing pivot left window")
		swingRight = flag.Int("htf-swing-right", 2, "HTF swing pivot right window")
	)
	flag.Parse()
	if *strategy == "" || *symbol == "" || *ltfPath == "" || *htfPath == "" {
		fmt.Fprintln(os.Stderr, "mtflab: -strategy, -symbol, -ltf-candles and -htf-candles are required")
		os.Exit(2)
	}
	spec, ok := instrument.Lookup(*symbol)
	if !ok {
		fmt.Fprintf(os.Stderr, "mtflab: %s has no verified precision\n", *symbol)
		os.Exit(1)
	}
	ltfInterval := feed.IntervalDuration(*ltf)
	htfInterval := feed.IntervalDuration(*htf)
	if ltfInterval <= 0 || htfInterval <= 0 {
		fmt.Fprintf(os.Stderr, "mtflab: unknown interval (ltf=%q htf=%q)\n", *ltf, *htf)
		os.Exit(1)
	}

	det, err := buildDetector(*strategy)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mtflab: %v\n", err)
		os.Exit(1)
	}

	symbolNorm := instrument.Normalize(*symbol)
	ltfCandles, err := readCandles(*ltfPath, spec.PriceScale, symbolNorm, ltfInterval)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mtflab: ltf candles: %v\n", err)
		os.Exit(1)
	}
	htfCandles, err := readCandles(*htfPath, spec.PriceScale, symbolNorm, htfInterval)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mtflab: htf candles: %v\n", err)
		os.Exit(1)
	}

	htfCfg := features.HTFTrendConfig{SwingLeft: *swingLeft, SwingRight: *swingRight}
	series := scan.BuildHTFTrendSeries(htfCandles, htfCfg)
	admit := scan.HTFTrendAdmit(series)

	params := scan.DefaultBTParams()
	params.Costs = scan.Costs{SpreadTicks: *spread, SlippageTicks: *slippage}
	params.SpreadTicks = *spread
	params.ExpiryBars = *expiry
	params.MaxHoldBars = *maxHold

	baseline := func(c []domain.Candle) (scan.BTResult, error) {
		r, _, err := scan.Backtest(symbolNorm, spec, ltfInterval, c, det, params)
		return r, err
	}
	filtered := func(c []domain.Candle) (scan.BTResult, error) {
		r, _, err := scan.BacktestFiltered(symbolNorm, spec, ltfInterval, c, det, params, admit)
		return r, err
	}

	fmt.Printf("mtflab  %s  %s  ltf=%s htf=%s  htf_swing=%d/%d\n", det.Name(), symbolNorm, *ltf, *htf, *swingLeft, *swingRight)
	fmt.Printf("ltf candles %d bars  htf candles %d bars  costs spread %d + 2x slip %d\n\n",
		len(ltfCandles), len(htfCandles), *spread, *slippage)

	report("BASELINE (no HTF filter)", ltfCandles, baseline)
	fmt.Println()
	report("HTF-FILTERED (trade only with HTF trend)", ltfCandles, filtered)
}

func report(label string, candles []domain.Candle, run func([]domain.Candle) (scan.BTResult, error)) {
	fmt.Println(label)
	full, err := run(candles)
	if err != nil {
		fmt.Printf("  %v\n", err)
		return
	}
	mid := len(candles) / 2
	firstHalf, errA := run(candles[:mid])
	secondHalf, errB := run(candles[mid:])

	hdr := fmt.Sprintf("  %-12s %7s %7s %7s %12s %7s %8s", "sample", "armed", "filled", "trades", "expectancy", "win%", "maxDD")
	fmt.Println(hdr)
	fmt.Println("  " + strings.Repeat("-", len(hdr)-2))
	row("full", full, nil)
	row("first half", firstHalf, errA)
	row("second half", secondHalf, errB)

	t := full.Report.Trades
	switch {
	case t < 30:
		fmt.Printf("  VERDICT: not measurable — %d trades.\n", t)
	case full.ExpectancyRBps <= 0:
		fmt.Printf("  VERDICT: negative. %+.3f R/trade over %d trades. Do not promote.\n", rr(full.ExpectancyRBps), t)
	case errA == nil && errB == nil && (firstHalf.ExpectancyRBps <= 0 || secondHalf.ExpectancyRBps <= 0):
		fmt.Printf("  VERDICT: not robust. Full %+.3f R, halves %+.3f / %+.3f R.\n",
			rr(full.ExpectancyRBps), rr(firstHalf.ExpectancyRBps), rr(secondHalf.ExpectancyRBps))
	default:
		fmt.Printf("  VERDICT: positive in both halves (%+.3f / %+.3f R).\n",
			rr(firstHalf.ExpectancyRBps), rr(secondHalf.ExpectancyRBps))
	}
}

func rr(bps int64) float64 { return float64(bps) / 10000 }

func row(label string, r scan.BTResult, err error) {
	if err != nil {
		fmt.Printf("  %-12s %s\n", label, err)
		return
	}
	win := 0.0
	if r.Report.Trades > 0 {
		win = float64(r.Wins) * 100 / float64(r.Report.Trades)
	}
	fmt.Printf("  %-12s %7d %7d %7d %+11.3fR %6.1f%% %7.1fR\n",
		label, r.Armed, r.Filled, r.Report.Trades, rr(r.ExpectancyRBps), win, rr(r.MaxDrawdownRBps))
}

func buildDetector(name string) (scan.Detector, error) {
	switch strings.ToLower(name) {
	case "continuation":
		return scan.Continuation{Config: features.DefaultContinuationConfig()}, nil
	case "ict":
		return scan.ICT{Config: features.DefaultICTConfig()}, nil
	case "amn":
		return scan.AMN{Config: features.DefaultAMNConfig()}, nil
	}
	return nil, fmt.Errorf("unsupported strategy %q (continuation|ict|amn)", name)
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
			continue // header row
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
