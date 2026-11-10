// Command seasonlab measures two specific, well-documented calendar/session
// anomalies against real candle data, the same signal-vs-drift discipline
// as ibsv2: full sample + both halves, net of a stated cost, with a
// trade-count floor before trusting any verdict.
//
//  1. Overnight vs intraday (Cooper & Cliff 2008 "Like Night and Day";
//     Lou, Polk & Skouras "A Tug of War"): the US equity premium is
//     documented as arising almost entirely overnight (close-to-open),
//     with intraday (open-to-close) returns flat or negative. Only
//     meaningful for instruments with a real session close -- run this on
//     indices, not on FX which trades near-continuously 24/5.
//  2. Turn-of-month (Ariel 1987; Lakonishok & Smidt 1988): returns are
//     concentrated in a ~4-day window from the last trading day of the
//     month through the first 3 trading days of the next month, versus
//     flat/lower returns the rest of the month.
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
)

type bar struct {
	t                time.Time
	open, close      float64
}

func main() {
	var (
		path      = flag.String("candles", "", "daily CSV candle file (required)")
		symbol    = flag.String("symbol", "", "label for output (required)")
		costBps   = flag.Float64("cost-bps", 2, "round-trip cost in bps of price")
		splitFrac = flag.Float64("split", 0.5, "fraction of bars in the first half")
		effect    = flag.String("effect", "both", "overnight | turn-of-month | both")
	)
	flag.Parse()
	if *path == "" || *symbol == "" {
		fmt.Fprintln(os.Stderr, "seasonlab: -candles and -symbol are required")
		os.Exit(2)
	}
	bars, err := readBars(*path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "seasonlab: %v\n", err)
		os.Exit(1)
	}
	if len(bars) < 30 {
		fmt.Fprintf(os.Stderr, "seasonlab: only %d bars, too few\n", len(bars))
		os.Exit(1)
	}

	fmt.Printf("seasonlab  %s  %d bars  %s .. %s  cost %.1fbp/leg\n\n",
		*symbol, len(bars), bars[0].t.Format("2006-01-02"), bars[len(bars)-1].t.Format("2006-01-02"), *costBps)

	if *effect == "overnight" || *effect == "both" {
		overnightVsIntraday(bars, *costBps, *splitFrac)
		fmt.Println()
	}
	if *effect == "turn-of-month" || *effect == "both" {
		turnOfMonth(bars, *costBps, *splitFrac)
	}
}

// overnightVsIntraday splits each day's close-to-close return into the
// close[t-1]->open[t] ("overnight") and open[t]->close[t] ("intraday")
// legs and reports the mean of each, full sample + halves. No position is
// actually held across both legs at once here -- they are reported
// separately because the claim being tested is specifically that one
// decomposes the other, not a combined strategy.
func overnightVsIntraday(bars []bar, costBps, splitFrac float64) {
	type leg struct {
		overnight, intraday float64 // bps
	}
	var legs []leg
	for i := 1; i < len(bars); i++ {
		prevClose := bars[i-1].close
		open := bars[i].open
		close := bars[i].close
		if prevClose == 0 || open == 0 {
			continue
		}
		overnight := (open-prevClose)/prevClose*10000 - costBps
		intraday := (close-open)/open*10000 - costBps
		legs = append(legs, leg{overnight: overnight, intraday: intraday})
	}
	mid := int(float64(len(legs)) * splitFrac)
	mean := func(ls []leg, pick func(leg) float64) float64 {
		var sum float64
		for _, l := range ls {
			sum += pick(l)
		}
		return sum / float64(len(ls))
	}
	fmt.Println("OVERNIGHT (close->open) vs INTRADAY (open->close), net of cost each leg:")
	fmt.Printf("  %-12s %7s %12s %12s\n", "sample", "n", "overnight", "intraday")
	fmt.Printf("  %-12s %7d %10.2fbp %10.2fbp\n", "full", len(legs),
		mean(legs, func(l leg) float64 { return l.overnight }), mean(legs, func(l leg) float64 { return l.intraday }))
	fmt.Printf("  %-12s %7d %10.2fbp %10.2fbp\n", "first half", mid,
		mean(legs[:mid], func(l leg) float64 { return l.overnight }), mean(legs[:mid], func(l leg) float64 { return l.intraday }))
	fmt.Printf("  %-12s %7d %10.2fbp %10.2fbp\n", "second half", len(legs)-mid,
		mean(legs[mid:], func(l leg) float64 { return l.overnight }), mean(legs[mid:], func(l leg) float64 { return l.intraday }))

	fullO, fullI := mean(legs, func(l leg) float64 { return l.overnight }), mean(legs, func(l leg) float64 { return l.intraday })
	firstO := mean(legs[:mid], func(l leg) float64 { return l.overnight })
	secondO := mean(legs[mid:], func(l leg) float64 { return l.overnight })
	switch {
	case len(legs) < 30:
		fmt.Println("  VERDICT: not measurable.")
	case fullO > 0 && fullI <= 0 && firstO > 0 && secondO > 0:
		fmt.Printf("  VERDICT: confirms the claim in both halves (overnight %+.2f/%+.2fbp, intraday flat/negative).\n", firstO, secondO)
	case fullO <= 0:
		fmt.Println("  VERDICT: does not confirm — overnight is not positive here.")
	default:
		fmt.Println("  VERDICT: not robust across halves.")
	}
}

// turnOfMonth classifies each close-to-close daily return as TOM (the last
// trading day of a month through the 3rd trading day of the next) or
// non-TOM, and reports the mean of each, full sample + halves.
func turnOfMonth(bars []bar, costBps, splitFrac float64) {
	type dayReturn struct {
		ret float64 // bps, net of cost
		tom bool
	}
	var days []dayReturn
	for i := 1; i < len(bars); i++ {
		prevClose := bars[i-1].close
		close := bars[i].close
		if prevClose == 0 {
			continue
		}
		ret := (close-prevClose)/prevClose*10000 - costBps
		days = append(days, dayReturn{ret: ret, tom: isTurnOfMonth(bars, i)})
	}
	mid := int(float64(len(days)) * splitFrac)
	summarize := func(label string, ds []dayReturn) (tomMean, nonTomMean float64, tomN, nonN int) {
		var tomSum, nonSum float64
		for _, d := range ds {
			if d.tom {
				tomSum += d.ret
				tomN++
			} else {
				nonSum += d.ret
				nonN++
			}
		}
		if tomN > 0 {
			tomMean = tomSum / float64(tomN)
		}
		if nonN > 0 {
			nonTomMean = nonSum / float64(nonN)
		}
		fmt.Printf("  %-12s TOM: %4d days %+8.2fbp/day   non-TOM: %4d days %+8.2fbp/day\n", label, tomN, tomMean, nonN, nonTomMean)
		return
	}
	fmt.Println("TURN-OF-MONTH (last trading day + first 3 of next month) vs rest of month:")
	fullTOM, fullNon, _, _ := summarize("full", days)
	firstTOM, firstNon, _, _ := summarize("first half", days[:mid])
	secondTOM, secondNon, _, _ := summarize("second half", days[mid:])

	fullEdge := fullTOM - fullNon
	firstEdge := firstTOM - firstNon
	secondEdge := secondTOM - secondNon
	fmt.Printf("  edge (TOM - non-TOM): full %+.2fbp, first half %+.2fbp, second half %+.2fbp\n",
		fullEdge, firstEdge, secondEdge)

	switch {
	case len(days) < 30:
		fmt.Println("  VERDICT: not measurable.")
	case fullEdge <= 0:
		fmt.Println("  VERDICT: does not confirm — TOM days are not better than the rest of the month.")
	case firstEdge <= 0 || secondEdge <= 0:
		fmt.Printf("  VERDICT: not robust — edge flips sign between halves (%+.2f / %+.2fbp).\n", firstEdge, secondEdge)
	default:
		fmt.Printf("  VERDICT: confirms the claim in both halves (edge %+.2f / %+.2fbp).\n", firstEdge, secondEdge)
	}
}

// isTurnOfMonth reports whether bars[i] is the last trading day of a month
// or one of the first 3 trading days of the following month, using only
// the calendar sequence actually present in the data (no assumption about
// which days the market is open).
func isTurnOfMonth(bars []bar, i int) bool {
	month := bars[i].t.Month()
	// Last trading day of its month: the NEXT bar is a different month (or
	// this is the last bar in the series).
	if i == len(bars)-1 || bars[i+1].t.Month() != month {
		return true
	}
	// First 3 trading days of a new month: walk back up to 3 bars; if any
	// of them started a new month relative to its predecessor, i is within
	// the first 3 days of that new month.
	monthStart := -1
	for j := i; j >= 0 && j > i-3; j-- {
		if j == 0 || bars[j].t.Month() != bars[j-1].t.Month() {
			monthStart = j
			break
		}
	}
	return monthStart != -1 && i-monthStart < 3
}

func readBars(path string) ([]bar, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	reader := csv.NewReader(file)
	reader.FieldsPerRecord = -1
	var out []bar
	for {
		rec, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if len(rec) < 5 {
			continue
		}
		ms, err := strconv.ParseInt(strings.TrimSpace(rec[0]), 10, 64)
		if err != nil {
			continue // header row
		}
		open, err1 := strconv.ParseFloat(strings.TrimSpace(rec[1]), 64)
		close, err2 := strconv.ParseFloat(strings.TrimSpace(rec[4]), 64)
		if err1 != nil || err2 != nil {
			continue
		}
		out = append(out, bar{t: time.UnixMilli(ms).UTC(), open: open, close: close})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s contained no valid bars", path)
	}
	return out, nil
}
