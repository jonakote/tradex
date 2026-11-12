// One-off research tool: dumps every ICT (FVG) setup the existing
// scan.ICT detector finds over a candle stream, to CSV. Used to test
// APA's specific addition (require an LTF market-structure-shift
// confirmation before filling the ICT limit entry, instead of a blind
// fill) against the EXISTING, already-tested ICT detector -- not a new
// reimplementation of FVG logic.
package main

import (
	"encoding/csv"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"tradex/internal/domain"
	"tradex/internal/features"
	"tradex/internal/instrument"
	"tradex/internal/strategy/scan"
)

func main() {
	symbol := os.Args[1]
	path := os.Args[2]
	outPath := os.Args[3]
	spec, _ := instrument.Lookup(symbol)
	candles, err := readCandles(path, spec.PriceScale, symbol)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	det := scan.ICT{Config: features.DefaultICTConfig()}
	req := det.RequiredBars()

	out, _ := os.Create(outPath)
	defer out.Close()
	w := csv.NewWriter(out)
	w.Write([]string{"detected_at_ms", "side", "entry", "stop", "target"})
	for i := req; i <= len(candles); i++ {
		window := candles[:i]
		setup, ok := det.Detect(spec, window, 0)
		if !ok {
			continue
		}
		w.Write([]string{
			strconv.FormatInt(setup.DetectedAt.UnixMilli(), 10),
			setup.Side,
			strconv.FormatFloat(spec.Price(int64(setup.Entry)), 'f', spec.Digits, 64),
			strconv.FormatFloat(spec.Price(int64(setup.Stop)), 'f', spec.Digits, 64),
			strconv.FormatFloat(spec.Price(int64(setup.Target)), 'f', spec.Digits, 64),
		})
	}
	w.Flush()
}

func readCandles(path string, scaleVal int64, symbol string) ([]domain.Candle, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	reader := csv.NewReader(file)
	reader.FieldsPerRecord = -1
	var outc []domain.Candle
	first := true
	for {
		rec, err := reader.Read()
		if err != nil {
			break
		}
		if first {
			first = false
			if len(rec) > 0 {
				if _, e := strconv.ParseFloat(rec[0], 64); e != nil {
					continue
				}
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
			v, _ := strconv.ParseFloat(strings.TrimSpace(rec[i+1]), 64)
			s := v * float64(scaleVal)
			if s >= 0 {
				p[i] = domain.Price(int64(s + 0.5))
			} else {
				p[i] = domain.Price(int64(s - 0.5))
			}
		}
		ot := time.UnixMilli(ms).UTC()
		dur := time.Hour
		c := domain.Candle{Source: "csv", Symbol: symbol, Timeframe: dur,
			OpenTime: ot, CloseTime: ot.Add(dur), Open: p[0], High: p[1], Low: p[2], Close: p[3], TickCount: 1}
		if c.Validate() == nil {
			outc = append(outc, c)
		}
	}
	return outc, nil
}
