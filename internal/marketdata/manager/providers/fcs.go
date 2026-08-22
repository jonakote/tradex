package providers

import (
	"context"
	"strings"
	"sync/atomic"
	"time"

	"tradex/internal/feed"
	"tradex/internal/marketdata/manager"
)

// FCSProvider wraps the existing feed.FCSClient as a MarketDataProvider.
type FCSProvider struct {
	client     *feed.FCSClient
	config     feed.FCSConfig
	callsMonth int64
	monthLimit int64
}

// NewFCSProvider creates a provider from an existing FCS client.
func NewFCSProvider(client *feed.FCSClient, cfg feed.FCSConfig) *FCSProvider {
	return &FCSProvider{
		client:     client,
		config:     cfg,
		monthLimit: 500,
	}
}

func (p *FCSProvider) Name() string { return "fcs" }

func (p *FCSProvider) Supports(instrument manager.Instrument) bool {
	// FCS free tier supports all asset classes
	switch instrument.Type {
	case manager.InstrumentForex:
		return true
	case manager.InstrumentMetal:
		return true // XAU/USD and XAG/USD
	case manager.InstrumentCommodity:
		return true // BRENT, WTI
	case manager.InstrumentIndex:
		return true // US100, etc.
	case manager.InstrumentStock:
		return true
	case manager.InstrumentCrypto:
		return true
	}
	return false
}

func (p *FCSProvider) GetCandles(ctx context.Context, req manager.CandleRequest) ([]manager.Candle, error) {
	interval := timeframeToFCS(req.Timeframe)
	if interval == "" {
		return nil, &manager.ProviderError{
			Provider: "fcs", StatusCode: 400,
			Message: "unsupported timeframe for FCS", Retryable: false,
		}
	}

	feedCandles, err := p.client.FetchCandles(ctx, req.Symbol)
	if err != nil {
		return nil, classifyFCSError(err)
	}

	atomic.AddInt64(&p.callsMonth, 1)

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
			Provider:  "fcs",
			FetchedAt: now,
		}
		candles = append(candles, mc)
	}
	return candles, nil
}

func (p *FCSProvider) GetQuote(ctx context.Context, req manager.QuoteRequest) (manager.Quote, error) {
	feedCandles, err := p.client.FetchCandles(ctx, req.Symbol)
	if err != nil {
		return manager.Quote{}, classifyFCSError(err)
	}
	atomic.AddInt64(&p.callsMonth, 1)

	if len(feedCandles) == 0 {
		return manager.Quote{}, &manager.ProviderError{
			Provider: "fcs", StatusCode: 404,
			Message: "no data available", Retryable: false,
		}
	}

	latest := feedCandles[len(feedCandles)-1]
	return manager.Quote{
		Symbol:    req.Symbol,
		Bid:       latest.Close,
		Ask:       latest.Close,
		Price:     latest.Close,
		Timestamp: latest.Time,
		Provider:  "fcs",
		FetchedAt: time.Now().UTC(),
	}, nil
}

func (p *FCSProvider) RemainingQuota() manager.ProviderQuota {
	used := atomic.LoadInt64(&p.callsMonth)
	remaining := p.monthLimit - used
	if remaining < 0 {
		remaining = 0
	}
	return manager.ProviderQuota{
		Provider:         "fcs",
		CreditsUsed:      used,
		CreditsRemaining: remaining,
		DailyLimit:       p.monthLimit / 30, // ~16/day
	}
}

func timeframeToFCS(tf manager.Timeframe) string {
	switch tf {
	case manager.Timeframe5m:
		return "5m"
	case manager.Timeframe15m:
		return "15m"
	case manager.Timeframe30m:
		return "30m"
	case manager.Timeframe1h:
		return "1h"
	case manager.Timeframe4h:
		return "4h"
	case manager.Timeframe1d:
		return "1d"
	case manager.Timeframe1w:
		return "1w"
	}
	return ""
}

func classifyFCSError(err error) error {
	msg := err.Error()
	if strings.Contains(msg, "429") || strings.Contains(msg, "rate") {
		return &manager.ProviderError{
			Provider: "fcs", StatusCode: 429,
			Message: msg, Retryable: true, RetryAfter: 120 * time.Second,
		}
	}
	if strings.Contains(msg, "401") || strings.Contains(msg, "Invalid API key") {
		return &manager.ProviderError{
			Provider: "fcs", StatusCode: 401,
			Message: msg, Retryable: false,
		}
	}
	return &manager.ProviderError{
		Provider: "fcs", StatusCode: 500,
		Message: msg, Retryable: true,
	}
}

var _ manager.MarketDataProvider = (*FCSProvider)(nil)
