// Command xsectlab measures a cross-sectional ranking strategy: at each
// rebalance, score every instrument in the universe from its own trailing
// history (no lookahead), rank them, go long the top K and short the
// bottom K in equal weight, and hold to the next rebalance. Because the
// basket is long-short and equal-weighted, common-mode drift across the
// universe cancels by construction — the mean basket return net of cost is
// itself the edge test, no separate drift baseline needed (contrast with
// IBS's single-instrument, directional signal-vs-drift test).
//
// This is a genuinely different strategy shape from anything else in this
// project: every other detector picks a side on ONE instrument from its
// own pattern. This picks relative strength ACROSS instruments. Measured
// with the same discipline as stratlab/mrlab: full sample + both halves,
// net of cost, a trade-count floor before trusting any verdict.
package main

import (
	"encoding/csv"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"tradex/internal/domain"
)

type series struct {
	symbol  string
	candles []domain.Candle
	byTime  map[int64]int // OpenTime unix -> index, for alignment
}

func main() {
	var (
		symbolsFlag = flag.String("symbols", "", "comma-separated SYMBOL=path.csv pairs (required)")
		costBps     = flag.Float64("cost-bps", 3, "round-trip cost in bps of price, per leg")
		lookback    = flag.Int("lookback", 60, "bars of trailing history scored at each rebalance")
		hold        = flag.Int("hold", 10, "bars held before the next rebalance")
		top         = flag.Int("top", 2, "number of instruments to go long")
		bottom      = flag.Int("bottom", 2, "number of instruments to go short")
		mode        = flag.String("mode", "momentum", "momentum | reversal")
		splitFrac   = flag.Float64("split", 0.5, "fraction of rebalance periods in the first half")
	)
	flag.Parse()
	if *symbolsFlag == "" {
		fmt.Fprintln(os.Stderr, "xsectlab: -symbols SYMBOL=path.csv,SYMBOL2=path2.csv,... is required")
		os.Exit(2)
	}
	sign := 1.0
	switch strings.ToLower(*mode) {
	case "momentum":
		sign = 1.0
	case "reversal":
		sign = -1.0
	default:
		fmt.Fprintf(os.Stderr, "xsectlab: -mode must be momentum or reversal, got %q\n", *mode)
		os.Exit(2)
	}

	var universe []series
	for _, pair := range strings.Split(*symbolsFlag, ",") {
		kv := strings.SplitN(pair, "=", 2)
		if len(kv) != 2 {
			fmt.Fprintf(os.Stderr, "xsectlab: bad -symbols entry %q (want SYMBOL=path.csv)\n", pair)
			os.Exit(2)
		}
		candles, err := readCandles(kv[1])
		if err != nil {
			fmt.Fprintf(os.Stderr, "xsectlab: %s: %v\n", kv[0], err)
			os.Exit(1)
		}
		byTime := make(map[int64]int, len(candles))
		for i, c := range candles {
			byTime[c.OpenTime.Unix()] = i
		}
		universe = append(universe, series{symbol: kv[0], candles: candles, byTime: byTime})
	}
	if len(universe) < *top+*bottom {
		fmt.Fprintf(os.Stderr, "xsectlab: universe has %d instruments, need at least top+bottom=%d\n",
			len(universe), *top+*bottom)
		os.Exit(1)
	}

	// Align on timestamps every instrument has, so a score at index i on any
	// instrument refers to the same point in calendar time everywhere.
	common := intersectTimes(universe)
	if len(common) < *lookback+*hold+1 {
		fmt.Fprintf(os.Stderr, "xsectlab: only %d common bars across the universe, need more than %d\n",
			len(common), *lookback+*hold)
		os.Exit(1)
	}
	sort.Slice(common, func(i, j int) bool { return common[i] < common[j] })

	type period struct {
		at         time.Time
		netReturn  float64 // bps, mean over all 2K legs
		longs      []string
		shorts     []string
	}
	var periods []period

	for i := *lookback; i+*hold < len(common); i += *hold {
		type scored struct {
			symbol string
			score  float64
			ok     bool
		}
		var scores []scored
		for _, s := range universe {
			idx, ok1 := s.byTime[common[i]]
			lookbackIdx, ok2 := s.byTime[common[i-*lookback]]
			if !ok1 || !ok2 {
				scores = append(scores, scored{symbol: s.symbol, ok: false})
				continue
			}
			base := float64(s.candles[lookbackIdx].Close)
			now := float64(s.candles[idx].Close)
			if base == 0 {
				scores = append(scores, scored{symbol: s.symbol, ok: false})
				continue
			}
			ret := (now - base) / base
			scores = append(scores, scored{symbol: s.symbol, score: sign * ret, ok: true})
		}
		var usable []scored
		for _, sc := range scores {
			if sc.ok {
				usable = append(usable, sc)
			}
		}
		if len(usable) < *top+*bottom {
			continue
		}
		sort.Slice(usable, func(a, b int) bool { return usable[a].score > usable[b].score })
		longSet := usable[:*top]
		shortSet := usable[len(usable)-*bottom:]

		exitIdx := i + *hold
		var legReturns []float64
		var longs, shorts []string
		apply := func(sym string, side float64) {
			for _, s := range universe {
				if s.symbol != sym {
					continue
				}
				entryIdx, ok1 := s.byTime[common[i]]
				closeIdx, ok2 := s.byTime[common[exitIdx]]
				if !ok1 || !ok2 {
					return
				}
				entry := float64(s.candles[entryIdx].Close)
				exit := float64(s.candles[closeIdx].Close)
				if entry == 0 {
					return
				}
				gross := (exit - entry) / entry * side
				net := gross - (*costBps)/10000 // one round-trip cost charged per leg
				legReturns = append(legReturns, net*10000)
			}
		}
		for _, sc := range longSet {
			apply(sc.symbol, 1)
			longs = append(longs, sc.symbol)
		}
		for _, sc := range shortSet {
			apply(sc.symbol, -1)
			shorts = append(shorts, sc.symbol)
		}
		if len(legReturns) == 0 {
			continue
		}
		var sum float64
		for _, r := range legReturns {
			sum += r
		}
		periods = append(periods, period{at: universe[0].candles[universe[0].byTime[common[i]]].OpenTime,
			netReturn: sum / float64(len(legReturns)), longs: longs, shorts: shorts})
	}

	if len(periods) == 0 {
		fmt.Fprintln(os.Stderr, "xsectlab: no rebalance periods produced a full basket")
		os.Exit(1)
	}

	summarize := func(label string, ps []period) {
		var sum float64
		wins := 0
		for _, p := range ps {
			sum += p.netReturn
			if p.netReturn > 0 {
				wins++
			}
		}
		mean := sum / float64(len(ps))
		win := float64(wins) * 100 / float64(len(ps))
		fmt.Printf("  %-12s %5d periods   mean %+8.2fbp/period   win%% %5.1f%%\n", label, len(ps), mean, win)
	}

	fmt.Printf("xsectlab  mode=%s  universe=%d  lookback=%d  hold=%d  top=%d  bottom=%d  cost=%.1fbp/leg\n",
		*mode, len(universe), *lookback, *hold, *top, *bottom, *costBps)
	fmt.Printf("rebalance periods: %d, %s .. %s\n\n", len(periods),
		periods[0].at.Format("2006-01-02"), periods[len(periods)-1].at.Format("2006-01-02"))

	mid := int(float64(len(periods)) * *splitFrac)
	summarize("full", periods)
	summarize("first half", periods[:mid])
	summarize("second half", periods[mid:])

	fmt.Println()
	fmt.Println("sample rebalances (first 3):")
	for i, p := range periods {
		if i >= 3 {
			break
		}
		fmt.Printf("  %s  long %v  short %v  net %+.2fbp\n", p.at.Format("2006-01-02"), p.longs, p.shorts, p.netReturn)
	}

	fullMean := 0.0
	for _, p := range periods {
		fullMean += p.netReturn
	}
	fullMean /= float64(len(periods))
	firstMean, secondMean := 0.0, 0.0
	for _, p := range periods[:mid] {
		firstMean += p.netReturn
	}
	firstMean /= float64(mid)
	for _, p := range periods[mid:] {
		secondMean += p.netReturn
	}
	secondMean /= float64(len(periods) - mid)

	fmt.Println()
	switch {
	case len(periods) < 30:
		fmt.Printf("VERDICT: not measurable — %d rebalance periods.\n", len(periods))
	case fullMean <= 0:
		fmt.Printf("VERDICT: negative. %+.2fbp/period over %d periods. Do not promote.\n", fullMean, len(periods))
	case (firstMean <= 0) || (secondMean <= 0):
		fmt.Printf("VERDICT: not robust. Full %+.2fbp, halves %+.2f / %+.2fbp.\n", fullMean, firstMean, secondMean)
	default:
		fmt.Printf("VERDICT: positive in both halves (%+.2f / %+.2fbp). Worth a second, independent universe.\n", firstMean, secondMean)
	}
}

func intersectTimes(universe []series) []int64 {
	counts := map[int64]int{}
	for _, s := range universe {
		for t := range s.byTime {
			counts[t]++
		}
	}
	var out []int64
	for t, n := range counts {
		if n == len(universe) {
			out = append(out, t)
		}
	}
	return out
}

func readCandles(path string) ([]domain.Candle, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	reader := csv.NewReader(file)
	reader.FieldsPerRecord = -1
	var out []domain.Candle
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
		var p [4]float64
		ok := true
		for i := 0; i < 4; i++ {
			v, err := strconv.ParseFloat(strings.TrimSpace(rec[i+1]), 64)
			if err != nil {
				ok = false
				break
			}
			p[i] = v
		}
		if !ok {
			continue
		}
		ot := time.UnixMilli(ms).UTC()
		// Prices kept as float "ticks" scaled by 1e8 so cross-instrument
		// precision differences never matter for a percentage-return score.
		scale := 1e8
		out = append(out, domain.Candle{
			OpenTime: ot, CloseTime: ot,
			Open:  domain.Price(int64(p[0] * scale)),
			High:  domain.Price(int64(p[1] * scale)),
			Low:   domain.Price(int64(p[2] * scale)),
			Close: domain.Price(int64(p[3] * scale)),
		})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s contained no valid candles", path)
	}
	return out, nil
}
