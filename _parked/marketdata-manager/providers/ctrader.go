package providers

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"sync/atomic"
	"time"

	"tradex/internal/marketdata/manager"
)

const cTraderProviderName = "ctrader"

// CTraderSymbol describes the broker-specific symbol identity needed to
// translate cTrader's numeric price representation into TradeX prices.
// Symbol IDs are account/server specific and must never be inferred by name.
type CTraderSymbol struct {
	SymbolID int64                  `json:"symbol_id"`
	Symbol   string                 `json:"symbol"`
	Type     manager.InstrumentType `json:"type"`
	Digits   int                    `json:"digits"`
}

func (s CTraderSymbol) Validate() error {
	if s.SymbolID <= 0 || s.Symbol == "" {
		return errors.New("cTrader symbol ID and symbol are required")
	}
	if s.Digits < 0 || s.Digits > 10 {
		return errors.New("cTrader symbol digits must be between 0 and 10")
	}
	if err := (manager.Instrument{Symbol: s.Symbol, Type: s.Type}).Validate(); err != nil {
		return err
	}
	return nil
}

// CTraderConfig configures the market-data side of a cTrader connection.
// Authentication and network transport are deliberately owned by the injected
// CTraderDataClient so this provider cannot accidentally create live orders.
type CTraderConfig struct {
	AccountID   string          `json:"account_id"`
	Environment string          `json:"environment"` // demo or live
	Symbols     []CTraderSymbol `json:"symbols"`
}

func (c CTraderConfig) Validate() error {
	if c.AccountID == "" {
		return errors.New("cTrader market-data account ID is required")
	}
	if c.Environment != "demo" && c.Environment != "live" {
		return errors.New("cTrader market-data environment must be demo or live")
	}
	if len(c.Symbols) == 0 {
		return errors.New("cTrader market-data requires at least one symbol")
	}
	seenNames := make(map[string]struct{}, len(c.Symbols))
	seenIDs := make(map[int64]struct{}, len(c.Symbols))
	for _, symbol := range c.Symbols {
		if err := symbol.Validate(); err != nil {
			return fmt.Errorf("validate cTrader symbol %q: %w", symbol.Symbol, err)
		}
		if _, exists := seenNames[symbol.Symbol]; exists {
			return fmt.Errorf("duplicate cTrader symbol %q", symbol.Symbol)
		}
		if _, exists := seenIDs[symbol.SymbolID]; exists {
			return fmt.Errorf("duplicate cTrader symbol ID %d", symbol.SymbolID)
		}
		seenNames[symbol.Symbol] = struct{}{}
		seenIDs[symbol.SymbolID] = struct{}{}
	}
	return nil
}

// CTraderPeriod is the period enum exposed by the cTrader Open API.
type CTraderPeriod string

const (
	CTraderPeriodM1  CTraderPeriod = "M1"
	CTraderPeriodM5  CTraderPeriod = "M5"
	CTraderPeriodM15 CTraderPeriod = "M15"
	CTraderPeriodM30 CTraderPeriod = "M30"
	CTraderPeriodH1  CTraderPeriod = "H1"
	CTraderPeriodH4  CTraderPeriod = "H4"
	CTraderPeriodD1  CTraderPeriod = "D1"
	CTraderPeriodW1  CTraderPeriod = "W1"
)

// CTraderSpot is a normalized representation of ProtoOASpotEvent. Bid and
// Ask use cTrader's integer price units; the symbol's Digits determines the
// scale. Timestamp is the broker event time, not local receipt time.
type CTraderSpot struct {
	SymbolID  int64
	Bid       int64
	Ask       int64
	Timestamp time.Time
}

// CTraderTrendbar is a normalized representation of ProtoOATrendbar. cTrader
// stores low plus deltas rather than four absolute OHLC prices.
type CTraderTrendbar struct {
	SymbolID   int64
	Period     CTraderPeriod
	OpenTime   time.Time
	Low        int64
	DeltaOpen  uint64
	DeltaHigh  uint64
	DeltaClose uint64
	Volume     uint64
}

// CTraderDataClient is the network boundary for cTrader market data. A real
// Open API transport can implement this interface using JSON or protobuf;
// tests and paper-forward runs can inject a deterministic implementation.
// There are intentionally no order methods here.
type CTraderDataClient interface {
	GetSpot(context.Context, string, int64) (CTraderSpot, error)
	GetTrendbars(context.Context, string, int64, CTraderPeriod, time.Time, time.Time, int) ([]CTraderTrendbar, error)
	SubscribeSpots(context.Context, string, []int64, func(CTraderSpot) error) error
	SubscribeTrendbars(context.Context, string, int64, CTraderPeriod, func(CTraderTrendbar) error) error
}

// CTraderProvider adapts cTrader market data to the central provider manager.
// It is data-only: it cannot submit, cancel, or mutate broker state.
type CTraderProvider struct {
	client   CTraderDataClient
	config   CTraderConfig
	byName   map[string]CTraderSymbol
	byID     map[int64]CTraderSymbol
	now      func() time.Time
	requests int64
}

func NewCTraderProvider(client CTraderDataClient, config CTraderConfig, clock func() time.Time) (*CTraderProvider, error) {
	if client == nil {
		return nil, errors.New("cTrader market-data client is required")
	}
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if clock == nil {
		clock = time.Now
	}
	provider := &CTraderProvider{
		client: client,
		config: config,
		byName: make(map[string]CTraderSymbol, len(config.Symbols)),
		byID:   make(map[int64]CTraderSymbol, len(config.Symbols)),
		now:    clock,
	}
	for _, symbol := range config.Symbols {
		provider.byName[symbol.Symbol] = symbol
		provider.byID[symbol.SymbolID] = symbol
	}
	return provider, nil
}

func (p *CTraderProvider) Name() string { return cTraderProviderName }

func (p *CTraderProvider) Supports(instrument manager.Instrument) bool {
	symbol, ok := p.byName[instrument.Symbol]
	return ok && symbol.Type == instrument.Type
}

func (p *CTraderProvider) GetCandles(ctx context.Context, req manager.CandleRequest) ([]manager.Candle, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	symbol, ok := p.byName[req.Symbol]
	if !ok {
		return nil, cTraderProviderError(404, "symbol is not configured", false)
	}
	period, ok := cTraderPeriodFor(req.Timeframe)
	if !ok {
		return nil, cTraderProviderError(400, "unsupported cTrader timeframe", false)
	}
	atomic.AddInt64(&p.requests, 1)
	trendbars, err := p.client.GetTrendbars(ctx, p.config.AccountID, symbol.SymbolID, period, req.From, req.To, req.Limit)
	if err != nil {
		return nil, cTraderProviderError(500, err.Error(), true)
	}
	candles, err := p.convertTrendbars(symbol, req.Timeframe, trendbars)
	if err != nil {
		return nil, cTraderProviderError(502, err.Error(), false)
	}
	sort.SliceStable(candles, func(i, j int) bool { return candles[i].OpenTime.Before(candles[j].OpenTime) })
	return candles, nil
}

func (p *CTraderProvider) GetQuote(ctx context.Context, req manager.QuoteRequest) (manager.Quote, error) {
	if req.Symbol == "" {
		return manager.Quote{}, errors.New("quote request requires symbol")
	}
	symbol, ok := p.byName[req.Symbol]
	if !ok {
		return manager.Quote{}, cTraderProviderError(404, "symbol is not configured", false)
	}
	atomic.AddInt64(&p.requests, 1)
	spot, err := p.client.GetSpot(ctx, p.config.AccountID, symbol.SymbolID)
	if err != nil {
		return manager.Quote{}, cTraderProviderError(500, err.Error(), true)
	}
	quote, err := p.convertSpot(symbol, spot)
	if err != nil {
		return manager.Quote{}, cTraderProviderError(502, err.Error(), false)
	}
	return quote, nil
}

// SubscribeQuotes subscribes to live bid/ask events for all configured
// symbols. The callback receives normalized manager quotes and should persist
// or publish them; it is not called by the request/response manager cache.
func (p *CTraderProvider) SubscribeQuotes(ctx context.Context, handler func(manager.Quote) error) error {
	if handler == nil {
		return errors.New("cTrader quote handler is required")
	}
	ids := make([]int64, 0, len(p.byID))
	for id := range p.byID {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return p.client.SubscribeSpots(ctx, p.config.AccountID, ids, func(spot CTraderSpot) error {
		symbol, ok := p.byID[spot.SymbolID]
		if !ok {
			return fmt.Errorf("cTrader spot event contains unknown symbol ID %d", spot.SymbolID)
		}
		quote, err := p.convertSpot(symbol, spot)
		if err != nil {
			return cTraderProviderError(502, err.Error(), false)
		}
		return handler(quote)
	})
}

// SubscribeCandles subscribes to closed/live trendbar events for one symbol
// and timeframe. The transport is responsible for maintaining the cTrader
// subscription and reconnecting the underlying session.
func (p *CTraderProvider) SubscribeCandles(ctx context.Context, symbolName string, timeframe manager.Timeframe, handler func(manager.Candle) error) error {
	if handler == nil {
		return errors.New("cTrader candle handler is required")
	}
	symbol, ok := p.byName[symbolName]
	if !ok {
		return cTraderProviderError(404, "symbol is not configured", false)
	}
	period, ok := cTraderPeriodFor(timeframe)
	if !ok {
		return cTraderProviderError(400, "unsupported cTrader timeframe", false)
	}
	return p.client.SubscribeTrendbars(ctx, p.config.AccountID, symbol.SymbolID, period, func(bar CTraderTrendbar) error {
		if bar.SymbolID != symbol.SymbolID || bar.Period != period {
			return fmt.Errorf("cTrader trendbar event identity mismatch")
		}
		candle, err := p.convertTrendbar(symbol, timeframe, bar)
		if err != nil {
			return cTraderProviderError(502, err.Error(), false)
		}
		return handler(candle)
	})
}

func (p *CTraderProvider) RemainingQuota() manager.ProviderQuota {
	return manager.ProviderQuota{
		Provider:    cTraderProviderName,
		CreditsUsed: atomic.LoadInt64(&p.requests),
	}
}

func (p *CTraderProvider) convertSpot(symbol CTraderSymbol, spot CTraderSpot) (manager.Quote, error) {
	if spot.SymbolID != symbol.SymbolID {
		return manager.Quote{}, errors.New("cTrader spot symbol ID does not match configured symbol")
	}
	if spot.Bid <= 0 || spot.Ask < spot.Bid {
		return manager.Quote{}, errors.New("invalid cTrader bid/ask")
	}
	if spot.Timestamp.IsZero() {
		return manager.Quote{}, errors.New("cTrader spot timestamp is required")
	}
	scale := cTraderPriceScale(symbol.Digits)
	bid := float64(spot.Bid) / scale
	ask := float64(spot.Ask) / scale
	return manager.Quote{
		Symbol:    symbol.Symbol,
		Bid:       bid,
		Ask:       ask,
		Price:     bid + (ask-bid)/2,
		Timestamp: spot.Timestamp.UTC(),
		Provider:  cTraderProviderName,
		FetchedAt: p.now().UTC(),
	}, nil
}

func (p *CTraderProvider) convertTrendbars(symbol CTraderSymbol, timeframe manager.Timeframe, bars []CTraderTrendbar) ([]manager.Candle, error) {
	candles := make([]manager.Candle, 0, len(bars))
	for _, bar := range bars {
		if bar.SymbolID != symbol.SymbolID {
			return nil, fmt.Errorf("cTrader trendbar symbol ID does not match configured symbol")
		}
		period, ok := cTraderPeriodFor(timeframe)
		if !ok || bar.Period != period {
			return nil, fmt.Errorf("cTrader trendbar period does not match requested timeframe")
		}
		candle, err := p.convertTrendbar(symbol, timeframe, bar)
		if err != nil {
			return nil, err
		}
		candles = append(candles, candle)
	}
	return candles, nil
}

func (p *CTraderProvider) convertTrendbar(symbol CTraderSymbol, timeframe manager.Timeframe, bar CTraderTrendbar) (manager.Candle, error) {
	if bar.OpenTime.IsZero() {
		return manager.Candle{}, errors.New("cTrader trendbar open time is required")
	}
	if bar.Low <= 0 || bar.Volume == 0 {
		return manager.Candle{}, errors.New("cTrader trendbar low and volume are required")
	}
	scale := cTraderPriceScale(symbol.Digits)
	low := float64(bar.Low) / scale
	open := float64(uint64(bar.Low)+bar.DeltaOpen) / scale
	high := float64(uint64(bar.Low)+bar.DeltaHigh) / scale
	close := float64(uint64(bar.Low)+bar.DeltaClose) / scale
	candle := manager.Candle{
		Symbol:    symbol.Symbol,
		Timeframe: timeframe,
		OpenTime:  bar.OpenTime.UTC(),
		Open:      open,
		High:      high,
		Low:       low,
		Close:     close,
		Volume:    float64(bar.Volume),
		Provider:  cTraderProviderName,
		FetchedAt: p.now().UTC(),
	}
	if err := candle.Validate(); err != nil {
		return manager.Candle{}, err
	}
	return candle, nil
}

func cTraderPriceScale(digits int) float64 { return math.Pow10(digits) }

func cTraderPeriodFor(timeframe manager.Timeframe) (CTraderPeriod, bool) {
	switch timeframe {
	case manager.Timeframe1m:
		return CTraderPeriodM1, true
	case manager.Timeframe5m:
		return CTraderPeriodM5, true
	case manager.Timeframe15m:
		return CTraderPeriodM15, true
	case manager.Timeframe30m:
		return CTraderPeriodM30, true
	case manager.Timeframe1h:
		return CTraderPeriodH1, true
	case manager.Timeframe4h:
		return CTraderPeriodH4, true
	case manager.Timeframe1d:
		return CTraderPeriodD1, true
	case manager.Timeframe1w:
		return CTraderPeriodW1, true
	default:
		return "", false
	}
}

func cTraderProviderError(status int, message string, retryable bool) *manager.ProviderError {
	return &manager.ProviderError{Provider: cTraderProviderName, StatusCode: status, Message: message, Retryable: retryable}
}

var _ manager.MarketDataProvider = (*CTraderProvider)(nil)
