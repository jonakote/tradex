// Package providers implements MarketDataProvider adapters for external data sources.
package providers

import (
	"context"
	"strings"
	"sync/atomic"
	"time"

	"tradex/internal/feed"
	"tradex/internal/marketdata/manager"
)

// TwelveDataProvider wraps the existing feed.TwelveDataClient as a MarketDataProvider.
type TwelveDataProvider struct {
	client      *feed.TwelveDataClient
	config      feed.TwelveDataConfig
	callsToday  int64
	dailyLimit  int64
	minuteLimit int
}

// NewTwelveDataProvider creates a provider from an existing Twelve Data client.
func NewTwelveDataProvider(client *feed.TwelveDataClient, cfg feed.TwelveDataConfig) *TwelveDataProvider {
	return &TwelveDataProvider{
		client:      client,
		config:      cfg,
		dailyLimit:  800,
		minuteLimit: 8,
	}
}

func (p *TwelveDataProvider) Name() string { return "twelvedata" }

func (p *TwelveDataProvider) Supports(instrument manager.Instrument) bool {
	// Twelve Data free tier supports: forex pairs, XAU/USD, US stocks/ETFs
	switch instrument.Type {
	case manager.InstrumentForex:
		return true
	case manager.InstrumentMetal:
		return instrument.Symbol == "XAU/USD" // XAG requires paid
	case manager.InstrumentStock:
		return true // US stocks and ETFs
	case manager.InstrumentIndex:
		return false // Indices require paid (use ETFs like QQQ instead)
	case manager.InstrumentCommodity:
		return false // Oil requires paid
	}
	return false
}

func (p *TwelveDataProvider) GetCandles(ctx context.Context, req manager.CandleRequest) ([]manager.Candle, error) {
	// Map manager timeframe to feed interval string
	interval := timeframeToTwelveData(req.Timeframe)
	if interval == "" {
		return nil, &manager.ProviderError{
			Provider: "twelvedata", StatusCode: 400,
			Message: "unsupported timeframe", Retryable: false,
		}
	}

	// Use the existing client which handles HTTP, parsing, dedup
	feedCandles, err := p.client.FetchCandles(ctx, req.Symbol)
	if err != nil {
		return nil, classifyTwelveDataError(err)
	}

	atomic.AddInt64(&p.callsToday, 1)

	// Convert feed.Candle to manager.Candle
	now := time.Now().UTC()
	candles := make([]manager.Candle, 0, len(feedCandles))
	for _, fc := range feedCandles {
		mc := manager.Candle{
			Symbol:    fc.Symbol,
			Timeframe: req.Timeframe,
			OpenTime:  fc.Time.Add(-req.Timeframe.Duration()),
			Open:      fc.Open,
			High:      fc.High,
			Low:       fc.Low,
			Close:     fc.Close,
			Volume:    fc.Volume,
			Provider:  "twelvedata",
			FetchedAt: now,
		}
		candles = append(candles, mc)
	}
	return candles, nil
}

func (p *TwelveDataProvider) GetQuote(ctx context.Context, req manager.QuoteRequest) (manager.Quote, error) {
	// Twelve Data doesn't have a dedicated quote endpoint on free tier.
	// Use latest candle close as an approximation.
	feedCandles, err := p.client.FetchCandles(ctx, req.Symbol)
	if err != nil {
		return manager.Quote{}, classifyTwelveDataError(err)
	}
	atomic.AddInt64(&p.callsToday, 1)

	if len(feedCandles) == 0 {
		return manager.Quote{}, &manager.ProviderError{
			Provider: "twelvedata", StatusCode: 404,
			Message: "no data available", Retryable: false,
		}
	}

	latest := feedCandles[len(feedCandles)-1]
	return manager.Quote{
		Symbol:    req.Symbol,
		Bid:       latest.Close, // Approximation
		Ask:       latest.Close,
		Price:     latest.Close,
		Timestamp: latest.Time,
		Provider:  "twelvedata",
		FetchedAt: time.Now().UTC(),
	}, nil
}

func (p *TwelveDataProvider) RemainingQuota() manager.ProviderQuota {
	used := atomic.LoadInt64(&p.callsToday)
	remaining := p.dailyLimit - used
	if remaining < 0 {
		remaining = 0
	}
	return manager.ProviderQuota{
		Provider:         "twelvedata",
		CreditsUsed:      used,
		CreditsRemaining: remaining,
		DailyLimit:       p.dailyLimit,
		MinuteLimit:      int64(p.minuteLimit),
	}
}

func timeframeToTwelveData(tf manager.Timeframe) string {
	switch tf {
	case manager.Timeframe1m:
		return "1min"
	case manager.Timeframe5m:
		return "5min"
	case manager.Timeframe15m:
		return "15min"
	case manager.Timeframe30m:
		return "30min"
	case manager.Timeframe1h:
		return "1h"
	case manager.Timeframe4h:
		return "4h"
	case manager.Timeframe1d:
		return "1day"
	case manager.Timeframe1w:
		return "1week"
	}
	return ""
}

func classifyTwelveDataError(err error) error {
	msg := err.Error()
	if strings.Contains(msg, "429") || strings.Contains(msg, "run out of API credits") {
		return &manager.ProviderError{
			Provider: "twelvedata", StatusCode: 429,
			Message: msg, Retryable: true, RetryAfter: 60 * time.Second,
		}
	}
	if strings.Contains(msg, "404") || strings.Contains(msg, "not available") {
		return &manager.ProviderError{
			Provider: "twelvedata", StatusCode: 404,
			Message: msg, Retryable: false,
		}
	}
	return &manager.ProviderError{
		Provider: "twelvedata", StatusCode: 500,
		Message: msg, Retryable: true,
	}
}

var _ manager.MarketDataProvider = (*TwelveDataProvider)(nil)
