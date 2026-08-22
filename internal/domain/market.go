package domain

import (
	"errors"
	"time"
)

type Quote struct {
	Source   string
	Symbol   string
	Bid      Price
	Ask      Price
	At       time.Time
	Sequence uint64
}

func (q Quote) Validate() error {
	if q.Source == "" || q.Symbol == "" {
		return errors.New("quote source and symbol are required")
	}
	if q.Bid <= 0 || q.Ask < q.Bid {
		return errors.New("invalid quote bid/ask")
	}
	if q.At.IsZero() {
		return errors.New("quote timestamp is required")
	}
	return nil
}

func (q Quote) Mid() Price { return q.Bid + (q.Ask-q.Bid)/2 }

type Candle struct {
	Source    string
	Symbol    string
	Timeframe time.Duration
	OpenTime  time.Time
	CloseTime time.Time
	Open      Price
	High      Price
	Low       Price
	Close     Price
	TickCount uint64
}

func (c Candle) Validate() error {
	if c.Source == "" || c.Symbol == "" {
		return errors.New("candle source and symbol are required")
	}
	if c.Timeframe <= 0 || c.OpenTime.IsZero() || c.CloseTime.IsZero() {
		return errors.New("candle timeframe and timestamps are required")
	}
	if !c.CloseTime.Equal(c.OpenTime.Add(c.Timeframe)) {
		return errors.New("candle close must equal open plus timeframe")
	}
	if c.Open <= 0 || c.High <= 0 || c.Low <= 0 || c.Close <= 0 {
		return errors.New("candle prices must be positive")
	}
	if c.High < c.Low || c.High < c.Open || c.High < c.Close || c.Low > c.Open || c.Low > c.Close {
		return errors.New("invalid candle OHLC geometry")
	}
	if c.TickCount == 0 {
		return errors.New("candle tick count must be positive")
	}
	return nil
}

func (c Candle) EventTime() time.Time { return c.CloseTime }
