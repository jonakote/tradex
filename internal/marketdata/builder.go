package marketdata

import (
	"errors"
	"fmt"
	"time"

	"tradex/internal/domain"
)

var (
	ErrQuoteOutOfOrder = errors.New("quote is duplicate or out of order")
	ErrBuilderFinished = errors.New("bar builder is finished")
)

type BarUpdate struct {
	Completed  *domain.Candle
	GapPeriods int
}

type BarBuilder struct {
	source       string
	symbol       string
	timeframe    time.Duration
	current      *domain.Candle
	lastAt       time.Time
	lastSequence uint64
	hasQuote     bool
	finished     bool
}

func NewBarBuilder(source, symbol string, timeframe time.Duration) (*BarBuilder, error) {
	if source == "" || symbol == "" || timeframe <= 0 {
		return nil, errors.New("source, symbol, and positive timeframe are required")
	}
	return &BarBuilder{source: source, symbol: symbol, timeframe: timeframe}, nil
}

func (b *BarBuilder) Push(quote domain.Quote) (BarUpdate, error) {
	if b.finished {
		return BarUpdate{}, ErrBuilderFinished
	}
	if err := quote.Validate(); err != nil {
		return BarUpdate{}, err
	}
	if quote.Source != b.source || quote.Symbol != b.symbol {
		return BarUpdate{}, errors.New("quote does not belong to builder stream")
	}
	if b.hasQuote && (quote.At.Before(b.lastAt) || quote.Sequence <= b.lastSequence) {
		return BarUpdate{}, ErrQuoteOutOfOrder
	}

	bucket := quote.At.UTC().Truncate(b.timeframe)
	price := quote.Mid()
	update := BarUpdate{}
	if b.current == nil {
		b.current = newCandle(b.source, b.symbol, b.timeframe, bucket, price)
	} else if bucket.Equal(b.current.OpenTime) {
		updateCandle(b.current, price)
	} else if bucket.After(b.current.OpenTime) {
		completed := *b.current
		update.Completed = &completed
		update.GapPeriods = int(bucket.Sub(b.current.CloseTime) / b.timeframe)
		b.current = newCandle(b.source, b.symbol, b.timeframe, bucket, price)
	} else {
		return BarUpdate{}, ErrQuoteOutOfOrder
	}

	b.lastAt = quote.At
	b.lastSequence = quote.Sequence
	b.hasQuote = true
	return update, nil
}

// Finish returns the final partial-or-complete bar and permanently closes the
// builder. The caller records that the source ended; Finish does not pretend a
// missing market tick occurred at the close boundary.
func (b *BarBuilder) Finish() (*domain.Candle, error) {
	if b.finished {
		return nil, ErrBuilderFinished
	}
	b.finished = true
	if b.current == nil {
		return nil, nil
	}
	completed := *b.current
	if err := completed.Validate(); err != nil {
		return nil, fmt.Errorf("finish candle: %w", err)
	}
	return &completed, nil
}

func newCandle(source, symbol string, timeframe time.Duration, openTime time.Time, price domain.Price) *domain.Candle {
	return &domain.Candle{
		Source: source, Symbol: symbol, Timeframe: timeframe,
		OpenTime: openTime, CloseTime: openTime.Add(timeframe),
		Open: price, High: price, Low: price, Close: price, TickCount: 1,
	}
}

func updateCandle(candle *domain.Candle, price domain.Price) {
	if price > candle.High {
		candle.High = price
	}
	if price < candle.Low {
		candle.Low = price
	}
	candle.Close = price
	candle.TickCount++
}
