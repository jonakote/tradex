package providers

import (
	"context"
	"strings"
	"sync/atomic"
	"time"

	"tradex/internal/feed"
	"tradex/internal/marketdata/manager"
)

// BinanceProvider wraps the public Binance Spot feed in the central market
// data provider interface. It is intentionally read-only.
type BinanceProvider struct {
	client      *feed.BinanceClient
	calls       int64
	minuteLimit int
}

func NewBinanceProvider(client *feed.BinanceClient) *BinanceProvider {
	return &BinanceProvider{client: client, minuteLimit: 600}
}

func (p *BinanceProvider) Name() string { return "binance" }

func (p *BinanceProvider) Supports(instrument manager.Instrument) bool {
	return instrument.Type == manager.InstrumentCrypto
}

func (p *BinanceProvider) GetCandles(ctx context.Context, req manager.CandleRequest) ([]manager.Candle, error) {
	interval := timeframeToBinance(req.Timeframe)
	if interval == "" {
		return nil, &manager.ProviderError{
			Provider: "binance", StatusCode: 400,
			Message: "unsupported timeframe for Binance", Retryable: false,
		}
	}

	feedCandles, err := p.client.FetchCandlesAt(ctx, req.Symbol, interval)
	if err != nil {
		return nil, classifyBinanceError(err)
	}
	atomic.AddInt64(&p.calls, 1)

	now := time.Now().UTC()
	candles := make([]manager.Candle, 0, len(feedCandles))
	for _, fc := range feedCandles {
		candles = append(candles, manager.Candle{
			Symbol:    fc.Symbol,
			Timeframe: req.Timeframe,
			OpenTime:  fc.OpenTime,
			Open:      fc.Open,
			High:      fc.High,
			Low:       fc.Low,
			Close:     fc.Close,
			Volume:    fc.Volume,
			Provider:  "binance",
			FetchedAt: now,
		})
	}
	return candles, nil
}

func (p *BinanceProvider) GetQuote(ctx context.Context, req manager.QuoteRequest) (manager.Quote, error) {
	q, err := p.client.FetchQuote(ctx, req.Symbol)
	if err != nil {
		return manager.Quote{}, classifyBinanceError(err)
	}
	atomic.AddInt64(&p.calls, 1)
	return manager.Quote{
		Symbol:    q.Symbol,
		Bid:       q.Bid,
		Ask:       q.Ask,
		Price:     q.Price,
		Timestamp: q.Time,
		Provider:  "binance",
		FetchedAt: time.Now().UTC(),
	}, nil
}

func (p *BinanceProvider) RemainingQuota() manager.ProviderQuota {
	used := atomic.LoadInt64(&p.calls)
	remaining := int64(p.minuteLimit) - used
	if remaining < 0 {
		remaining = 0
	}
	return manager.ProviderQuota{
		Provider:         "binance",
		CreditsUsed:      used,
		CreditsRemaining: remaining,
		MinuteLimit:      int64(p.minuteLimit),
	}
}

func timeframeToBinance(tf manager.Timeframe) string {
	switch tf {
	case manager.Timeframe1m:
		return "1m"
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

func classifyBinanceError(err error) error {
	msg := err.Error()
	if strings.Contains(msg, "429") || strings.Contains(msg, "418") {
		return &manager.ProviderError{
			Provider: "binance", StatusCode: 429,
			Message: msg, Retryable: true, RetryAfter: time.Minute,
		}
	}
	if strings.Contains(msg, "400") || strings.Contains(msg, "Invalid symbol") {
		return &manager.ProviderError{
			Provider: "binance", StatusCode: 400,
			Message: msg, Retryable: false,
		}
	}
	return &manager.ProviderError{
		Provider: "binance", StatusCode: 500,
		Message: msg, Retryable: true,
	}
}

var _ manager.MarketDataProvider = (*BinanceProvider)(nil)
