package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"tradex/internal/feed"
	"tradex/internal/instrument"
)

// runPrice answers "what is the price right now" for one or more instruments.
//
// It prints two different things on purpose, because conflating them is how a
// strategy ends up acting on a price that no longer exists:
//
//   - the live venue quote (bid/ask), which is what you could deal at now;
//   - the newest CLOSED candle, which is the newest bar a strategy is allowed
//     to see. On a 1h timeframe that bar can legitimately be nearly an hour
//     old, and the in-progress bar is deliberately withheld because a signal
//     derived from it would repaint as the bar develops.
//
// Market data only. Credentials come from the environment.
func runPrice(args []string, stdout, stderr io.Writer) int {
	symbols := args
	if len(symbols) == 0 {
		symbols = []string{"EURUSD"}
	}
	for i, symbol := range symbols {
		canonical := instrument.Normalize(symbol)
		if _, ok := instrument.Lookup(canonical); !ok {
			fmt.Fprintf(stderr, "tradex price: %s has no verified precision; known: %v\n",
				symbol, instrument.Symbols())
			return 1
		}
		if _, ok := instrument.CapitalEpic(canonical); !ok {
			fmt.Fprintf(stderr, "tradex price: %s has no verified capital.com epic\n", symbol)
			return 1
		}
		symbols[i] = canonical
	}

	source, err := feed.NewCapital(feed.CapitalConfig{
		APIKey:     os.Getenv("CAPITAL_API_KEY"),
		Identifier: os.Getenv("CAPITAL_IDENTIFIER"),
		Password:   os.Getenv("CAPITAL_PASSWORD"),
		BaseURL:    os.Getenv("CAPITAL_BASE_URL"),
		Symbols:    symbols,
		Interval:   "1h",
		Bars:       3,
	})
	if err != nil {
		fmt.Fprintf(stderr, "tradex price: %v\n", err)
		return 1
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	now := time.Now().UTC()
	fmt.Fprintf(stdout, "capital.com  %s\n\n", now.Format("2006-01-02 15:04:05 UTC"))

	status := 0
	for _, symbol := range symbols {
		spec, _ := instrument.Lookup(symbol)

		quote, err := source.Snapshot(ctx, symbol)
		if err != nil {
			fmt.Fprintf(stderr, "%-8s live quote failed: %v\n", symbol, err)
			status = 1
			continue
		}
		spread := int64(quote.Ask - quote.Bid)
		fmt.Fprintf(stdout, "%-8s bid %.*f  ask %.*f  spread %d ticks",
			symbol, spec.Digits, spec.Price(int64(quote.Bid)),
			spec.Digits, spec.Price(int64(quote.Ask)), spread)
		if spec.HasPipConvention() {
			fmt.Fprintf(stdout, " (%.1f pips)", float64(spread)*spec.TickSize()/spec.PipSize)
		}
		age := now.Sub(quote.At).Round(time.Second)
		if age < 0 {
			// The venue's clock can sit a fraction ahead of ours; a
			// negative age is a clock skew artefact, not a future quote.
			age = 0
		}
		fmt.Fprintf(stdout, "\n         quoted at %s (%s ago)\n",
			quote.At.Format("15:04:05"), age)

		marketStatus, err := source.MarketStatus(ctx, symbol)
		if err == nil && marketStatus != "TRADEABLE" {
			fmt.Fprintf(stdout, "         market status %s\n", marketStatus)
		}

		// The newest bar a strategy may act on. Request a window wide
		// enough to span a weekend so a Monday-morning call is not empty.
		to := now
		from := to.Add(-96 * time.Hour)
		candles, err := source.History(ctx, symbol, from, to)
		if err != nil {
			fmt.Fprintf(stderr, "%-8s history failed: %v\n", symbol, err)
			status = 1
			continue
		}
		if len(candles) == 0 {
			fmt.Fprintf(stdout, "         no closed 1h bar in the last 96h\n\n")
			continue
		}
		last := candles[len(candles)-1]
		fmt.Fprintf(stdout, "         newest closed 1h bar  %s  O %.*f  H %.*f  L %.*f  C %.*f\n",
			last.OpenTime.Format("2006-01-02 15:04"),
			spec.Digits, spec.Price(int64(last.Open)),
			spec.Digits, spec.Price(int64(last.High)),
			spec.Digits, spec.Price(int64(last.Low)),
			spec.Digits, spec.Price(int64(last.Close)))
		fmt.Fprintf(stdout, "         that bar closed %s ago; the forming bar is withheld by design\n\n",
			now.Sub(last.CloseTime).Round(time.Second))
	}
	return status
}
