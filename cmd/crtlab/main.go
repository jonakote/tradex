// Command crtlab measures the Candle Range Theory strategy over historical
// candles. It is research-only: it reads CSV, runs the real state machine,
// and prints a report. It opens no broker connection and places no order.
//
// Usage:
//
//	crtlab -symbol EURUSD -scale 100000 \
//	  -setup data/EURUSD_4h.csv -setup-tf 4h \
//	  -confirm data/EURUSD_15m.csv -confirm-tf 15m \
//	  -spread-ticks 2 -slippage-ticks 1
//
// CSV layout: open_time_ms,open,high,low,close,volume (header required).
package main

import (
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"time"

	"tradex/internal/domain"
	"tradex/internal/instrument"
	"tradex/internal/research/crtlab"
)

func main() {
	var (
		symbol     = flag.String("symbol", "", "instrument symbol (required)")
		scale      = flag.Int64("scale", 0, "price scale override; 0 resolves the verified scale from internal/instrument")
		setupPath  = flag.String("setup", "", "setup-timeframe CSV (required)")
		setupTF    = flag.Duration("setup-tf", 4*time.Hour, "setup timeframe")
		confPath   = flag.String("confirm", "", "confirmation-timeframe CSV (required)")
		confTF     = flag.Duration("confirm-tf", 15*time.Minute, "confirmation timeframe")
		spread     = flag.Int64("spread-ticks", 2, "spread charged per closed trade, in ticks")
		slippage   = flag.Int64("slippage-ticks", 1, "slippage charged on entry and on exit, in ticks")
		minRange   = flag.Int64("min-range-ticks", 0, "minimum range-candle height (0 = scale-derived default)")
		minRR      = flag.Int64("min-rr-bps", 15000, "minimum reward/risk in basis points")
		maxPurge   = flag.Int64("max-purge-bps", 5000, "maximum purge depth as bps of range height")
		minReclaim = flag.Int64("min-reclaim-bps", 2500, "minimum close-back-inside distance, bps of range")
		trendGate  = flag.Bool("trend-align", false, "require higher-timeframe swing-trend alignment")
		holdBars   = flag.Int("max-hold-bars", 2, "setup bars a filled position may be held")
		jsonOut    = flag.String("json", "", "optional path for the full JSON result")
		tradesOut  = flag.String("trades-csv", "", "optional path for a per-trade CSV")
	)
	flag.Parse()

	if *symbol == "" || *setupPath == "" || *confPath == "" {
		fmt.Fprintln(os.Stderr, "symbol, setup, and confirm are required")
		flag.Usage()
		os.Exit(2)
	}

	// Resolve the price scale from the verified registry so a research run
	// and the live engine cannot disagree about what a tick means. The flag
	// stays available for CSV from an unregistered source.
	if *scale <= 0 {
		spec, ok := instrument.Lookup(*symbol)
		if !ok {
			exitOn(fmt.Errorf("no verified precision for %q; pass -scale explicitly "+
				"or add the instrument to internal/instrument", *symbol))
		}
		*scale = spec.PriceScale
		fmt.Fprintf(os.Stderr, "resolved %s precision: %d digits, scale %d\n",
			spec.Symbol, spec.Digits, spec.PriceScale)
	}

	params := crtlab.DefaultParams()
	params.Strategy.SetupTimeframe = *setupTF
	params.Strategy.ConfirmationTimeframe = *confTF
	params.Strategy.RequireTrendAlignment = *trendGate
	params.Strategy.MaxHoldSetupBars = *holdBars
	params.Strategy.Detector.MinRewardRiskBps = *minRR
	params.Strategy.Detector.MaxPurgeBps = *maxPurge
	params.Strategy.Detector.MinReclaimBps = *minReclaim
	// A fixed tick count means nothing across instruments; scale the floor so
	// the same invocation is sane on a 5-digit FX pair and on crypto cents.
	params.Strategy.Detector.MinRangeTicks = *minRange
	if *minRange <= 0 {
		params.Strategy.Detector.MinRangeTicks = defaultMinRange(*scale)
	}
	params.Strategy.Detector.StopBufferTicks = *spread
	params.Costs = crtlab.Costs{SpreadTicks: *spread, SlippageTicks: *slippage}

	setupSeries, err := loadSeries(*setupPath, *symbol, *setupTF, *scale)
	exitOn(err)
	confSeries, err := loadSeries(*confPath, *symbol, *confTF, *scale)
	exitOn(err)

	result, _, err := crtlab.Run(setupSeries, confSeries, params)
	exitOn(err)

	printReport(result, params)

	if *jsonOut != "" {
		payload := struct {
			Params crtlab.Params `json:"params"`
			Result crtlab.Result `json:"result"`
		}{params, result}
		encoded, err := json.MarshalIndent(payload, "", "  ")
		exitOn(err)
		exitOn(os.WriteFile(*jsonOut, append(encoded, '\n'), 0o644))
		fmt.Printf("\nwrote %s\n", *jsonOut)
	}
	if *tradesOut != "" {
		exitOn(writeTrades(*tradesOut, result))
		fmt.Printf("wrote %s\n", *tradesOut)
	}
}

// defaultMinRange requires a range candle worth at least 10 pips on a 5-digit
// FX quote, and the scale-equivalent elsewhere.
func defaultMinRange(scale int64) int64 {
	minimum := scale / 1000
	if minimum < 1 {
		return 1
	}
	return minimum
}

func loadSeries(path, symbol string, timeframe time.Duration, scale int64) (crtlab.Series, error) {
	file, err := os.Open(path)
	if err != nil {
		return crtlab.Series{}, err
	}
	defer file.Close()

	reader := csv.NewReader(file)
	rows, err := reader.ReadAll()
	if err != nil {
		return crtlab.Series{}, err
	}
	if len(rows) < 2 {
		return crtlab.Series{}, fmt.Errorf("%s: no data rows", path)
	}

	series := crtlab.Series{Source: "csv", Symbol: symbol, Timeframe: timeframe}
	toPrice := func(text string) (domain.Price, error) {
		value, err := strconv.ParseFloat(text, 64)
		if err != nil {
			return 0, err
		}
		return domain.Price(math.Round(value * float64(scale))), nil
	}

	for i, row := range rows[1:] {
		if len(row) < 5 {
			return crtlab.Series{}, fmt.Errorf("%s row %d: want at least 5 columns", path, i+2)
		}
		openMS, err := strconv.ParseInt(row[0], 10, 64)
		if err != nil {
			return crtlab.Series{}, fmt.Errorf("%s row %d: %w", path, i+2, err)
		}
		open, err := toPrice(row[1])
		if err != nil {
			return crtlab.Series{}, fmt.Errorf("%s row %d: %w", path, i+2, err)
		}
		high, err := toPrice(row[2])
		if err != nil {
			return crtlab.Series{}, fmt.Errorf("%s row %d: %w", path, i+2, err)
		}
		low, err := toPrice(row[3])
		if err != nil {
			return crtlab.Series{}, fmt.Errorf("%s row %d: %w", path, i+2, err)
		}
		closePrice, err := toPrice(row[4])
		if err != nil {
			return crtlab.Series{}, fmt.Errorf("%s row %d: %w", path, i+2, err)
		}
		bar := crtlab.Bar{
			OpenTime: time.UnixMilli(openMS).UTC(),
			Open:     open, High: high, Low: low, Close: closePrice, Ticks: 1,
		}
		// Rounding can collapse a doji; clamp rather than reject so a single
		// flat bar does not abort a multi-year run.
		if bar.High < bar.Open {
			bar.High = bar.Open
		}
		if bar.High < bar.Close {
			bar.High = bar.Close
		}
		if bar.Low > bar.Open {
			bar.Low = bar.Open
		}
		if bar.Low > bar.Close {
			bar.Low = bar.Close
		}
		series.Bars = append(series.Bars, bar)
	}
	return series, nil
}

func printReport(result crtlab.Result, params crtlab.Params) {
	report := result.Report
	fmt.Printf("CANDLE RANGE THEORY — %s (%s)\n", result.Symbol, result.SpecVersion)
	fmt.Printf("window            %s .. %s\n",
		result.From.Format("2006-01-02"), result.To.Format("2006-01-02"))
	if result.Funnel.Uncovered > 0 {
		fmt.Printf("coverage          %d patterns skipped: no confirmation data in their fill window\n",
			result.Funnel.Uncovered)
	}
	fmt.Printf("setup tf %s / confirm tf %s | trend gate %v | min R:R %.2f | costs %d ticks\n",
		params.Strategy.SetupTimeframe, params.Strategy.ConfirmationTimeframe,
		params.Strategy.RequireTrendAlignment,
		float64(params.Strategy.Detector.MinRewardRiskBps)/10000,
		params.Costs.SpreadTicks+2*params.Costs.SlippageTicks)

	fmt.Printf("\nOPPORTUNITY FUNNEL\n")
	fmt.Printf("  setup bars scanned   %d\n", result.Funnel.SetupBars)
	fmt.Printf("  patterns armed       %d\n", result.Funnel.Armed)
	if result.Funnel.Uncovered > 0 {
		fmt.Printf("  skipped: no data     %d\n", result.Funnel.Uncovered)
	}
	fmt.Printf("  limits filled        %d\n", result.Funnel.Filled)
	fmt.Printf("  expired unfilled     %d\n", result.Funnel.Expired)
	fmt.Printf("  target / stop / time %d / %d / %d\n",
		result.Funnel.TakeProfit, result.Funnel.StopLoss, result.Funnel.TimeStop)

	fmt.Printf("\nPERFORMANCE (net of costs, 1R risked per trade)\n")
	if report.Trades == 0 {
		fmt.Printf("  no closed trades\n")
		return
	}
	fmt.Printf("  closed trades        %d\n", report.Trades)
	fmt.Printf("  win rate             %.1f%%  (95%% CI %.1f%% .. %.1f%%)\n",
		report.WinRate*100, report.WinRateLower95*100, report.WinRateUpper95*100)
	fmt.Printf("  expectancy           %+.3f R per trade\n", float64(result.ExpectancyRBps)/10000)
	fmt.Printf("  total return         %+.2f R\n", float64(result.NetRBps)/10000)
	if report.ProfitFactorInfinite {
		fmt.Printf("  profit factor        infinite (no losing trade)\n")
	} else {
		fmt.Printf("  profit factor        %.3f\n", report.ProfitFactor)
	}
	fmt.Printf("  max drawdown         %.2f R\n", float64(result.MaxDrawdownRBps)/10000)
	fmt.Printf("  max losing streak    %d\n", report.MaxConsecutiveLosses)

	fmt.Printf("\nBY DIRECTION\n")
	kinds := make([]string, 0, len(result.ByKind))
	for kind := range result.ByKind {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	for _, kind := range kinds {
		bucket := result.ByKind[kind]
		fmt.Printf("  %-22s %4d trades  %5.1f%% win  %+.3f R/trade\n",
			kind, bucket.Trades, 100*float64(bucket.Wins)/float64(bucket.Trades),
			float64(bucket.ExpectancyBps)/10000)
	}

	fmt.Printf("\nBY SETUP HOUR (UTC, purge-candle close)\n")
	hours := make([]int, 0, len(result.ByHour))
	for hour := range result.ByHour {
		hours = append(hours, hour)
	}
	sort.Ints(hours)
	for _, hour := range hours {
		bucket := result.ByHour[hour]
		fmt.Printf("  %02d:00  %4d trades  %5.1f%% win  %+.3f R/trade  %+.1f R total\n",
			hour, bucket.Trades, 100*float64(bucket.Wins)/float64(bucket.Trades),
			float64(bucket.ExpectancyBps)/10000, float64(bucket.NetRBps)/10000)
	}
}

func writeTrades(path string, result crtlab.Result) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()
	writer := csv.NewWriter(file)
	defer writer.Flush()
	if err := writer.Write([]string{
		"symbol", "kind", "outcome", "detected_at", "resolved_at", "gross_r", "net_r", "pnl_minor",
	}); err != nil {
		return err
	}
	for _, trade := range result.Trades {
		if err := writer.Write([]string{
			trade.Symbol, string(trade.Kind), string(trade.Outcome),
			trade.DetectedAt.Format(time.RFC3339), trade.ResolvedAt.Format(time.RFC3339),
			strconv.FormatFloat(float64(trade.GrossRBps)/10000, 'f', 4, 64),
			strconv.FormatFloat(float64(trade.NetRBps)/10000, 'f', 4, 64),
			strconv.FormatInt(trade.PnLMinor, 10),
		}); err != nil {
			return err
		}
	}
	return nil
}

func exitOn(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
