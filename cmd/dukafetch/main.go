// Command dukafetch downloads historical candles from Dukascopy's public
// data feed and writes the CSV layout the research harnesses read.
//
// It is research tooling: read-only, no account, no order, no subscription.
//
// Usage:
//
//	dukafetch -symbol EURUSD -timeframe h1 -from 2015-01-01 -to 2026-11-01 \
//	  -side bid -out data/EURUSD_h1.csv -cache ~/.cache/dukascopy
//
// Request cost differs sharply by timeframe, because of how the feed packages
// files: d1 is one request per year, h1 one per month, m1 one per day. Prefer
// the coarsest timeframe that answers the question, and always use -cache.
package main

import (
	"encoding/csv"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"tradex/internal/domain"
	"tradex/internal/feed/dukascopy"
)

func main() {
	var (
		symbol    = flag.String("symbol", "", "instrument, e.g. EURUSD (required)")
		timeframe = flag.String("timeframe", "h1", "d1, h1, or m1")
		side      = flag.String("side", "bid", "bid, ask, or mid")
		fromText  = flag.String("from", "", "inclusive UTC start date YYYY-MM-DD (required)")
		toText    = flag.String("to", "", "exclusive UTC end date YYYY-MM-DD (required)")
		out       = flag.String("out", "", "output CSV path (required)")
		cacheDir  = flag.String("cache", "", "directory for raw .bi5 caching (strongly recommended)")
		list      = flag.Bool("list", false, "list supported instruments and exit")
	)
	flag.Parse()

	if *list {
		for _, name := range dukascopy.Symbols() {
			instrument, err := dukascopy.Lookup(name)
			if err != nil {
				continue
			}
			fmt.Printf("%-8s digits=%d scale=%d\n", name, instrument.Digits, instrument.PriceScale)
		}
		return
	}
	if *symbol == "" || *fromText == "" || *toText == "" || *out == "" {
		fmt.Fprintln(os.Stderr, "symbol, from, to, and out are required")
		flag.Usage()
		os.Exit(2)
	}

	from, err := time.Parse("2006-01-02", *fromText)
	exitOn(err)
	to, err := time.Parse("2006-01-02", *toText)
	exitOn(err)

	instrument, err := dukascopy.Lookup(*symbol)
	exitOn(err)

	client := dukascopy.NewClient(*cacheDir)
	started := time.Now()
	fmt.Fprintf(os.Stderr, "fetching %s %s %s from %s to %s\n",
		instrument.Symbol, *timeframe, *side, *fromText, *toText)

	candles, err := client.Candles(instrument.Symbol,
		dukascopy.Timeframe(strings.ToLower(*timeframe)),
		dukascopy.Side(strings.ToLower(*side)), from, to)
	exitOn(err)
	if len(candles) == 0 {
		exitOn(fmt.Errorf("no candles returned for %s in that range", instrument.Symbol))
	}

	exitOn(writeCSV(*out, candles, instrument))
	fmt.Printf("%s: %d candles %s .. %s -> %s (%.0fs)\n",
		instrument.Symbol, len(candles),
		candles[0].OpenTime.Format("2006-01-02"),
		candles[len(candles)-1].OpenTime.Format("2006-01-02"),
		*out, time.Since(started).Seconds())
	fmt.Printf("use -scale %d with crtlab so the decimal prices round-trip exactly\n",
		instrument.PriceScale)
}

// writeCSV emits open_time_ms,open,high,low,close,volume. Prices are printed
// at exactly the instrument's precision, so multiplying by its price scale
// recovers the original integer tick without rounding error.
func writeCSV(path string, candles []domain.Candle, instrument dukascopy.Instrument) error {
	if directory := filepath.Dir(path); directory != "" {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			return err
		}
	}
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()

	writer := csv.NewWriter(file)
	defer writer.Flush()
	if err := writer.Write([]string{
		"open_time_ms", "open", "high", "low", "close", "volume",
	}); err != nil {
		return err
	}
	format := func(price domain.Price) string {
		return strconv.FormatFloat(float64(price)/float64(instrument.PriceScale),
			'f', instrument.Digits, 64)
	}
	for _, candle := range candles {
		if err := writer.Write([]string{
			strconv.FormatInt(candle.OpenTime.UnixMilli(), 10),
			format(candle.Open), format(candle.High), format(candle.Low), format(candle.Close),
			strconv.FormatUint(candle.TickCount, 10),
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
