package main

import (
	"bufio"
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"tradex/internal/feed"
	"tradex/internal/instrument"
)

// runCandles exports closed candles to CSV for the research harnesses.
//
// Format matches what crtlab and contlab read:
//
//	open_time_ms,open,high,low,close,volume
//
// Prices are written as decimals at the instrument's verified precision, so
// the file is readable and round-trips exactly through the same scale.
func runCandles(args []string, stdout, stderr io.Writer) int {
	if len(args) < 5 {
		fmt.Fprintln(stderr,
			"usage: tradex candles <symbol> <interval> <from YYYY-MM-DD> <to YYYY-MM-DD> <out.csv>")
		return 2
	}
	symbol := instrument.Normalize(args[0])
	interval, fromText, toText, outPath := args[1], args[2], args[3], args[4]

	spec, ok := instrument.Lookup(symbol)
	if !ok {
		fmt.Fprintf(stderr, "tradex candles: %s has no verified precision\n", args[0])
		return 1
	}
	from, err := time.Parse("2006-01-02", fromText)
	if err != nil {
		fmt.Fprintf(stderr, "tradex candles: bad from date %q\n", fromText)
		return 1
	}
	to, err := time.Parse("2006-01-02", toText)
	if err != nil {
		fmt.Fprintf(stderr, "tradex candles: bad to date %q\n", toText)
		return 1
	}
	if !to.After(from) {
		fmt.Fprintln(stderr, "tradex candles: to must be after from")
		return 1
	}

	source, err := feed.NewCapital(feed.CapitalConfig{
		APIKey:     os.Getenv("CAPITAL_API_KEY"),
		Identifier: os.Getenv("CAPITAL_IDENTIFIER"),
		Password:   os.Getenv("CAPITAL_PASSWORD"),
		BaseURL:    os.Getenv("CAPITAL_BASE_URL"),
		Symbols:    []string{symbol},
		Interval:   interval,
		Side:       "bid",
	})
	if err != nil {
		fmt.Fprintf(stderr, "tradex candles: %v\n", err)
		return 1
	}

	// Exclusive create: a dataset a study already consumed must never be
	// silently replaced underneath it.
	file, err := os.OpenFile(outPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		fmt.Fprintf(stderr, "tradex candles: %v\n", err)
		return 1
	}
	defer file.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	fmt.Fprintf(stdout, "fetching %s %s from %s to %s\n", symbol, interval, fromText, toText)
	candles, err := source.History(ctx, symbol, from.UTC(), to.UTC())
	if err != nil {
		fmt.Fprintf(stderr, "tradex candles: %v\n", err)
		return 1
	}
	if len(candles) == 0 {
		fmt.Fprintln(stderr, "tradex candles: the venue returned no bars for that window")
		return 1
	}

	writer := bufio.NewWriter(file)
	csvWriter := csv.NewWriter(writer)
	_ = csvWriter.Write([]string{"open_time_ms", "open", "high", "low", "close", "volume"})
	price := func(ticks int64) string {
		return strconv.FormatFloat(spec.Price(ticks), 'f', spec.Digits, 64)
	}
	for _, candle := range candles {
		_ = csvWriter.Write([]string{
			strconv.FormatInt(candle.OpenTime.UnixMilli(), 10),
			price(int64(candle.Open)), price(int64(candle.High)),
			price(int64(candle.Low)), price(int64(candle.Close)),
			strconv.FormatUint(candle.TickCount, 10),
		})
	}
	csvWriter.Flush()
	if err := csvWriter.Error(); err != nil {
		fmt.Fprintf(stderr, "tradex candles: %v\n", err)
		return 1
	}
	if err := writer.Flush(); err != nil {
		fmt.Fprintf(stderr, "tradex candles: %v\n", err)
		return 1
	}

	first, last := candles[0].OpenTime, candles[len(candles)-1].OpenTime
	fmt.Fprintf(stdout, "%d bars %s .. %s -> %s\n", len(candles),
		first.Format("2006-01-02 15:04"), last.Format("2006-01-02 15:04"), outPath)
	fmt.Fprintf(stdout, "scale: %d digits (use -scale %d)\n", spec.Digits, spec.PriceScale)

	// Report the obvious vendor defect immediately, so a bad dataset is
	// caught here and not after a study has been run on it.
	duplicates := 0
	for _, candle := range candles {
		if candle.Open == candle.Close {
			duplicates++
		}
	}
	share := float64(duplicates) * 100 / float64(len(candles))
	fmt.Fprintf(stdout, "open == close on %d bars (%.1f%%)\n", duplicates, share)
	if share > 5 {
		fmt.Fprintln(stderr,
			"WARNING: a high open==close rate means the vendor is duplicating the open "+
				"into the close; do not measure a close-reading strategy on this file")
	}
	if strings.EqualFold(interval, "1h") && len(candles) < 100 {
		fmt.Fprintln(stderr, "WARNING: fewer than 100 bars; check the window")
	}
	return 0
}
