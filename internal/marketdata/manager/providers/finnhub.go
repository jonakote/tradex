package providers

import (
	"context"
	"strings"
	"sync/atomic"
	"time"

	"tradex/internal/feed"
	"tradex/internal/marketdata/manager"
)

// FinnhubProvider wraps the feed.FinnhubClient as a MarketDataProvider.
type FinnhubProvider struct {
	client      *feed.FinnhubClient
	config      feed.FinnhubConfig
	callsToday  int64
	dailyLimit  int64
	minuteLimit int
}

// NewFinnhubProvider creates a provider from an existing Finnhub client.
func NewFinnhubProvider(client *feed.FinnhubClient, cfg feed.FinnhubConfig) *FinnhubProvider {
	return &FinnhubProvider{
		client:      client,
		config:      cfg,
		dailyLimit:  86400, // free tier: 60/min, effectively ~86k/day
		minuteLimit: 60,
	}
}

func (p *FinnhubProvider) Name() string { return "finnhub" }

func (p *FinnhubProvider) Supports(instrument manager.Instrument) bool {
	switch instrument.Type {
	case manager.InstrumentForex:
		return true // via OANDA forex candles
	case manager.InstrumentMetal:
		return instrument.Symbol == "XAU/USD" // OANDA:XAU_USD; XAG not on free tier
	case manager.InstrumentStock:
		return true // US stocks and ETFs
	case manager.InstrumentCrypto:
		return false // requires separate crypto endpoints
	case manager.InstrumentIndex:
		return false // use ETFs like QQQ instead
	case manager.InstrumentCommodity:
		return false
	}
	return false
}

func (p *FinnhubProvider) GetCandles(ctx context.Context, req manager.CandleRequest) ([]manager.Candle, error) {
	interval := timeframeToFinnhub(req.Timeframe)
	if interval == "" {
		return nil, &manager.ProviderError{
			Provider: "finnhub", StatusCode: 400,
			Message: "unsupported timeframe for Finnhub", Retryable: false,
		}
	}

	feedCandles, err := p.client.FetchCandlesAt(ctx, req.Symbol, interval)
	if err != nil {
		return nil, classifyFinnhubError(err)
	}

	atomic.AddInt64(&p.callsToday, 1)

	now := time.Now().UTC()
	candles := make([]manager.Candle, 0, len(feedCandles))
	for _, fc := range feedCandles {
		mc := manager.Candle{
			Symbol:    fc.Symbol,
			Timeframe: req.Timeframe,
			OpenTime:  fc.Time,
			Open:      fc.Open,
			High:      fc.High,
			Low:       fc.Low,
			Close:     fc.Close,
			Volume:    fc.Volume,
			Provider:  "finnhub",
			FetchedAt: now,
		}
		candles = append(candles, mc)
	}
	return candles, nil
}

func (p *FinnhubProvider) GetQuote(ctx context.Context, req manager.QuoteRequest) (manager.Quote, error) {
	q, err := p.client.FetchQuote(ctx, req.Symbol)
	if err != nil {
		return manager.Quote{}, classifyFinnhubError(err)
	}
	atomic.AddInt64(&p.callsToday, 1)

	return manager.Quote{
		Symbol:    q.Symbol,
		Bid:       q.Bid,
		Ask:       q.Ask,
		Price:     q.Price,
		Timestamp: q.Time,
		Provider:  "finnhub",
		FetchedAt: time.Now().UTC(),
	}, nil
}

func (p *FinnhubProvider) RemainingQuota() manager.ProviderQuota {
	used := atomic.LoadInt64(&p.callsToday)
	remaining := p.dailyLimit - used
	if remaining < 0 {
		remaining = 0
	}
	return manager.ProviderQuota{
		Provider:         "finnhub",
		CreditsUsed:      used,
		CreditsRemaining: remaining,
		DailyLimit:       p.dailyLimit,
		MinuteLimit:      int64(p.minuteLimit),
	}
}

func timeframeToFinnhub(tf manager.Timeframe) string {
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
	case manager.Timeframe1d:
		return "1day"
	}
	return ""
}

func classifyFinnhubError(err error) error {
	msg := err.Error()
	if strings.Contains(msg, "429") || strings.Contains(msg, "rate limited") {
		return &manager.ProviderError{
			Provider: "finnhub", StatusCode: 429,
			Message: msg, Retryable: true, RetryAfter: time.Minute,
		}
	}
	if strings.Contains(msg, "401") || strings.Contains(msg, "403") ||
		strings.Contains(msg, "Invalid API key") {
		return &manager.ProviderError{
			Provider: "finnhub", StatusCode: 401,
			Message: msg, Retryable: false,
		}
	}
	if strings.Contains(msg, "no data") {
		return &manager.ProviderError{
			Provider: "finnhub", StatusCode: 404,
			Message: msg, Retryable: false,
		}
	}
	return &manager.ProviderError{
		Provider: "finnhub", StatusCode: 500,
		Message: msg, Retryable: true,
	}
}
