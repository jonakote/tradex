package paper

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"sync"
	"time"

	"tradex/internal/clock"
	"tradex/internal/domain"
	"tradex/internal/economics"
)

var (
	ErrUnknownOrder        = errors.New("paper order not found")
	ErrOrderNotCancellable = errors.New("paper order is no longer cancellable")
)

type RolloverConfig struct {
	Enabled                   bool         `json:"enabled"`
	MinuteUTC                 int          `json:"minute_utc"`
	ChargeWeekdays            [7]bool      `json:"charge_weekdays"`
	TripleWeekday             time.Weekday `json:"triple_weekday"`
	TripleMultiplier          int          `json:"triple_multiplier"`
	LongPerQuantityStepMinor  int64        `json:"long_per_quantity_step_minor"`
	ShortPerQuantityStepMinor int64        `json:"short_per_quantity_step_minor"`
}

func (c RolloverConfig) Validate() error {
	if !c.Enabled {
		if c != (RolloverConfig{}) {
			return errors.New("disabled paper rollover must not carry assumptions")
		}
		return nil
	}
	if c.MinuteUTC < 0 || c.MinuteUTC >= 24*60 || c.TripleWeekday < time.Sunday ||
		c.TripleWeekday > time.Saturday || c.TripleMultiplier < 1 {
		return errors.New("invalid paper rollover configuration")
	}
	hasChargeDay := false
	for _, enabled := range c.ChargeWeekdays {
		hasChargeDay = hasChargeDay || enabled
	}
	if !hasChargeDay {
		return errors.New("paper rollover requires at least one charge weekday")
	}
	return nil
}

var _ domain.Broker = (*Broker)(nil)

type InstrumentConfig struct {
	Instrument                     domain.Instrument
	SlippageTicks                  int64
	HalfSpreadTicksForCandle       int64
	CommissionPerQuantityStepMinor int64
	PriceValue                     economics.PriceValue
	SubmissionLatency              time.Duration
	Rollover                       RolloverConfig
}

func (c InstrumentConfig) Validate() error {
	if err := c.Instrument.Validate(); err != nil {
		return err
	}
	if c.SlippageTicks < 0 || c.HalfSpreadTicksForCandle < 0 || c.SubmissionLatency < 0 ||
		c.CommissionPerQuantityStepMinor < 0 {
		return errors.New("invalid paper instrument costs")
	}
	if err := c.PriceValue.Validate(); err != nil {
		return err
	}
	return c.Rollover.Validate()
}

type Config struct {
	AccountID   string
	MaxQuoteAge time.Duration
	Instruments map[string]InstrumentConfig
}

func (c Config) Validate() error {
	if c.AccountID == "" || c.MaxQuoteAge <= 0 || len(c.Instruments) == 0 {
		return errors.New("paper account, quote age, and instruments are required")
	}
	for symbol, instrument := range c.Instruments {
		if symbol != instrument.Instrument.Symbol {
			return fmt.Errorf("paper instrument key %s does not match symbol %s", symbol, instrument.Instrument.Symbol)
		}
		if err := instrument.Validate(); err != nil {
			return fmt.Errorf("paper instrument %s: %w", symbol, err)
		}
	}
	return nil
}

type Broker struct {
	mu             sync.Mutex
	config         Config
	clock          clock.Clock
	quotes         map[string]domain.Quote
	orders         map[string]domain.BrokerOrder
	orderIDs       []string
	intentIndex    map[string]string
	approvals      map[string]domain.ApprovedOrder
	positions      map[string]domain.BrokerPosition
	positionIDs    []string
	orderPosition  map[string]string
	eligibleAt     map[string]time.Time
	marketOpen     map[string]bool
	marketStatusAt map[string]time.Time
	fills          []domain.Fill
	sequence       uint64
}

func New(config Config, sourceClock clock.Clock) (*Broker, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if sourceClock == nil {
		return nil, errors.New("paper broker clock is required")
	}
	return &Broker{
		config: config, clock: sourceClock, quotes: make(map[string]domain.Quote),
		orders: make(map[string]domain.BrokerOrder), intentIndex: make(map[string]string),
		approvals: make(map[string]domain.ApprovedOrder), positions: make(map[string]domain.BrokerPosition),
		orderPosition: make(map[string]string), eligibleAt: make(map[string]time.Time), marketOpen: make(map[string]bool),
		marketStatusAt: make(map[string]time.Time),
	}, nil
}

func (b *Broker) Submit(ctx context.Context, approved domain.ApprovedOrder) (domain.BrokerOrder, error) {
	if err := ctx.Err(); err != nil {
		return domain.BrokerOrder{}, err
	}
	if err := approved.Validate(); err != nil {
		return domain.BrokerOrder{}, err
	}
	if approved.Intent.AccountID != b.config.AccountID {
		return domain.BrokerOrder{}, errors.New("approved order belongs to another paper account")
	}
	instrument, ok := b.config.Instruments[approved.Intent.Symbol]
	if !ok {
		return domain.BrokerOrder{}, errors.New("paper instrument is not configured")
	}
	if approved.Intent.Quantity < instrument.Instrument.MinQuantity ||
		approved.Intent.Quantity > instrument.Instrument.MaxQuantity ||
		approved.Intent.Quantity%instrument.Instrument.QuantityStep != 0 {
		return domain.BrokerOrder{}, errors.New("approved quantity violates paper instrument limits")
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	if existingID, exists := b.intentIndex[approved.Intent.ID]; exists {
		if !sameApproval(b.approvals[existingID], approved) {
			return domain.BrokerOrder{}, errors.New("intent ID was reused with different content")
		}
		return b.orders[existingID], nil
	}

	now := b.clock.Now().UTC()
	if now.After(approved.Intent.ExpiresAt) {
		return domain.BrokerOrder{}, errors.New("approved order expired before submission")
	}
	eligibleAt := now.Add(instrument.SubmissionLatency)
	b.sequence++
	orderID := fmt.Sprintf("paper-order-%06d", b.sequence)
	order := domain.BrokerOrder{
		BrokerOrderID: orderID, IntentID: approved.Intent.ID, AccountID: approved.Intent.AccountID,
		Symbol: approved.Intent.Symbol, Side: approved.Intent.Side, Type: approved.Intent.Type,
		Quantity: approved.Intent.Quantity, Remaining: approved.Intent.Quantity,
		EntryPrice: approved.Intent.EntryPrice, StopLoss: approved.Intent.StopLoss,
		TakeProfit: approved.Intent.TakeProfit, Status: domain.OrderAcknowledged,
		SubmittedAt: now, UpdatedAt: now,
	}
	b.orders[orderID] = order
	b.orderIDs = append(b.orderIDs, orderID)
	b.intentIndex[approved.Intent.ID] = orderID
	b.approvals[orderID] = approved
	b.eligibleAt[orderID] = eligibleAt
	if !b.isMarketOpen(order.Symbol) {
		order.Status = domain.OrderRejected
		order.RejectReason = "MARKET_CLOSED"
		b.orders[orderID] = order
		return order, nil
	}
	if eligibleAt.After(approved.Intent.ExpiresAt) {
		order.Status = domain.OrderRejected
		order.RejectReason = "LATENCY_EXCEEDS_INTENT_TTL"
		b.orders[orderID] = order
		return order, nil
	}

	if quote, exists := b.quotes[order.Symbol]; exists && !now.Before(quote.At) && now.Sub(quote.At) <= b.config.MaxQuoteAge &&
		!now.Before(eligibleAt) {
		if _, err := b.tryFillEntry(orderID, quote, now, -1); err != nil {
			return domain.BrokerOrder{}, err
		}
	}
	return b.orders[orderID], nil
}

func (b *Broker) SetMarketOpen(symbol string, open bool, at time.Time) error {
	if at.IsZero() {
		return errors.New("paper market status time is required")
	}
	if _, exists := b.config.Instruments[symbol]; !exists {
		return errors.New("paper market status symbol is not configured")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if previous := b.marketStatusAt[symbol]; !previous.IsZero() && at.Before(previous) {
		return errors.New("paper market status is out of order")
	}
	b.marketOpen[symbol] = open
	b.marketStatusAt[symbol] = at.UTC()
	return nil
}

func (b *Broker) Orders() []domain.BrokerOrder {
	b.mu.Lock()
	defer b.mu.Unlock()
	result := make([]domain.BrokerOrder, 0, len(b.orderIDs))
	for _, id := range b.orderIDs {
		result = append(result, b.orders[id])
	}
	return result
}

func (b *Broker) isMarketOpen(symbol string) bool {
	open, exists := b.marketOpen[symbol]
	return !exists || open
}

func (b *Broker) Cancel(ctx context.Context, brokerOrderID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	order, exists := b.orders[brokerOrderID]
	if !exists {
		return ErrUnknownOrder
	}
	if order.Status == domain.OrderCancelled {
		return nil
	}
	if order.Status != domain.OrderAcknowledged && order.Status != domain.OrderPartiallyFilled {
		return ErrOrderNotCancellable
	}
	order.Status = domain.OrderCancelled
	order.UpdatedAt = b.clock.Now().UTC()
	b.orders[brokerOrderID] = order
	return nil
}

func (b *Broker) OpenOrders(ctx context.Context) ([]domain.BrokerOrder, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	result := make([]domain.BrokerOrder, 0)
	for _, id := range b.orderIDs {
		order := b.orders[id]
		if order.Status == domain.OrderAcknowledged || order.Status == domain.OrderPartiallyFilled {
			result = append(result, order)
		}
	}
	return result, nil
}

func (b *Broker) Positions(ctx context.Context) ([]domain.BrokerPosition, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	result := make([]domain.BrokerPosition, 0, len(b.positions))
	for _, id := range b.positionIDs {
		if position, exists := b.positions[id]; exists {
			result = append(result, position)
		}
	}
	return result, nil
}

func (b *Broker) Fills() []domain.Fill {
	b.mu.Lock()
	defer b.mu.Unlock()
	result := make([]domain.Fill, len(b.fills))
	copy(result, b.fills)
	return result
}

func (b *Broker) OnQuote(quote domain.Quote) ([]domain.Fill, error) {
	return b.onQuote(quote, -1)
}

// OnQuoteWithLiquidity applies a deterministic quantity cap to each eligible
// order. A zero cap represents a valid quote with no executable quantity.
func (b *Broker) OnQuoteWithLiquidity(quote domain.Quote, maxQuantityPerOrder int64) ([]domain.Fill, error) {
	if maxQuantityPerOrder < 0 {
		return nil, errors.New("paper quote liquidity cannot be negative")
	}
	return b.onQuote(quote, maxQuantityPerOrder)
}

func (b *Broker) onQuote(quote domain.Quote, maxQuantityPerOrder int64) ([]domain.Fill, error) {
	if err := quote.Validate(); err != nil {
		return nil, err
	}
	if _, exists := b.config.Instruments[quote.Symbol]; !exists {
		return nil, errors.New("quote symbol is not configured")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if previous, exists := b.quotes[quote.Symbol]; exists &&
		(quote.At.Before(previous.At) || quote.Sequence <= previous.Sequence) {
		return nil, errors.New("paper quote is duplicate or out of order")
	}
	b.quotes[quote.Symbol] = quote
	start := len(b.fills)
	if !b.isMarketOpen(quote.Symbol) {
		return nil, nil
	}
	if err := b.accrueRollover(quote.Symbol, quote.At); err != nil {
		return nil, err
	}
	for _, id := range b.orderIDs {
		order := b.orders[id]
		if order.Symbol == quote.Symbol && (order.Status == domain.OrderAcknowledged || order.Status == domain.OrderPartiallyFilled) {
			if quote.At.After(b.approvals[id].Intent.ExpiresAt) {
				if order.Status == domain.OrderPartiallyFilled {
					order.Status = domain.OrderCancelled
				} else {
					order.Status = domain.OrderRejected
					order.RejectReason = "INTENT_EXPIRED"
				}
				order.UpdatedAt = quote.At
				b.orders[id] = order
				continue
			}
			if quote.At.Before(b.eligibleAt[id]) {
				continue
			}
			if _, err := b.tryFillEntry(id, quote, quote.At, maxQuantityPerOrder); err != nil {
				return nil, err
			}
		}
	}
	for _, id := range b.positionIDs {
		position, exists := b.positions[id]
		if !exists || position.Symbol != quote.Symbol || !position.OpenedAt.Before(quote.At) {
			continue
		}
		if err := b.tryCloseOnQuote(id, quote); err != nil {
			return nil, err
		}
	}
	return copyFills(b.fills[start:]), nil
}

func (b *Broker) OnCandle(candle domain.Candle) ([]domain.Fill, error) {
	config, exists := b.config.Instruments[candle.Symbol]
	if !exists {
		return nil, errors.New("candle symbol is not configured")
	}
	return b.OnCandleWithSpread(candle, config.HalfSpreadTicksForCandle)
}

// OnCandleWithSpread makes the synthetic half-spread assumption explicit for
// this candle, allowing historical spread series to vary it event by event.
func (b *Broker) OnCandleWithSpread(candle domain.Candle, halfSpreadTicks int64) ([]domain.Fill, error) {
	if err := candle.Validate(); err != nil {
		return nil, err
	}
	if _, exists := b.config.Instruments[candle.Symbol]; !exists {
		return nil, errors.New("candle symbol is not configured")
	}
	if halfSpreadTicks < 0 {
		return nil, errors.New("paper candle half-spread cannot be negative")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.isMarketOpen(candle.Symbol) {
		return nil, nil
	}
	if err := b.accrueRollover(candle.Symbol, candle.CloseTime); err != nil {
		return nil, err
	}
	start := len(b.fills)
	for _, id := range b.positionIDs {
		position, exists := b.positions[id]
		if !exists || position.Symbol != candle.Symbol || position.OpenedAt.After(candle.OpenTime) {
			continue
		}
		if err := b.tryCloseOnCandle(id, candle, halfSpreadTicks); err != nil {
			return nil, err
		}
	}
	return copyFills(b.fills[start:]), nil
}

func (b *Broker) tryFillEntry(orderID string, quote domain.Quote, fillAt time.Time, maxQuantity int64) (*domain.Fill, error) {
	order := b.orders[orderID]
	if order.Status != domain.OrderAcknowledged && order.Status != domain.OrderPartiallyFilled {
		return nil, nil
	}
	config := b.config.Instruments[order.Symbol]
	price, fill := entryFillPrice(order, quote, config)
	if !fill {
		return nil, nil
	}
	if price <= 0 {
		return nil, errors.New("paper entry fill price is not positive")
	}
	protectionInvalid := (order.Side == domain.SideBuy && (order.StopLoss >= price || order.TakeProfit <= price)) ||
		(order.Side == domain.SideSell && (order.StopLoss <= price || order.TakeProfit >= price))
	if protectionInvalid {
		if order.Status == domain.OrderPartiallyFilled {
			order.Status = domain.OrderCancelled
		} else {
			order.Status = domain.OrderRejected
		}
		order.RejectReason = "PROTECTIVE_LEVELS_INVALID_AT_FILL"
		order.UpdatedAt = fillAt
		b.orders[orderID] = order
		return nil, nil
	}
	fillQuantity := order.Remaining
	if maxQuantity >= 0 && maxQuantity < fillQuantity {
		fillQuantity = maxQuantity - maxQuantity%config.Instrument.QuantityStep
	}
	if fillQuantity <= 0 {
		return nil, nil
	}
	commission, err := commissionFor(fillQuantity, config)
	if err != nil {
		return nil, err
	}
	positionID, exists := b.orderPosition[orderID]
	if !exists {
		b.sequence++
		positionID = fmt.Sprintf("paper-position-%06d", b.sequence)
		position := domain.BrokerPosition{
			BrokerPositionID: positionID, BrokerOrderID: orderID, IntentID: order.IntentID, AccountID: order.AccountID,
			Symbol: order.Symbol, Side: order.Side, Quantity: fillQuantity, EntryPrice: price,
			StopLoss: order.StopLoss, TakeProfit: order.TakeProfit, OpenedAt: fillAt,
			EntryCommissionMinor: commission, LastRolloverAt: fillAt,
		}
		b.positions[positionID] = position
		b.positionIDs = append(b.positionIDs, positionID)
		b.orderPosition[orderID] = positionID
	} else {
		position := b.positions[positionID]
		totalQuantity, ok := checkedSum(position.Quantity, fillQuantity)
		if !ok || totalQuantity <= 0 {
			return nil, errors.New("paper position quantity overflow")
		}
		average, err := weightedEntryPrice(position.EntryPrice, position.Quantity, price, fillQuantity, position.Side)
		if err != nil {
			return nil, err
		}
		position.EntryPrice = average
		position.Quantity = totalQuantity
		position.EntryCommissionMinor, err = addMinor(position.EntryCommissionMinor, commission)
		if err != nil {
			return nil, err
		}
		b.positions[positionID] = position
	}
	b.sequence++
	fillID := fmt.Sprintf("paper-fill-%06d", b.sequence)
	fillEvent := domain.Fill{
		FillID: fillID, BrokerOrderID: orderID, BrokerPositionID: positionID,
		IntentID: order.IntentID, AccountID: order.AccountID, Symbol: order.Symbol,
		Side: order.Side, Role: domain.FillEntry, Quantity: fillQuantity, Price: price,
		CommissionMinor: commission, RealizedNetMinor: -commission, FilledAt: fillAt,
	}
	b.fills = append(b.fills, fillEvent)
	order.Remaining -= fillQuantity
	if order.Remaining == 0 {
		order.Status = domain.OrderFilled
	} else {
		order.Status = domain.OrderPartiallyFilled
	}
	order.UpdatedAt = fillAt
	b.orders[orderID] = order
	return &fillEvent, nil
}

func weightedEntryPrice(existing domain.Price, existingQuantity int64, next domain.Price, nextQuantity int64,
	side domain.Side) (domain.Price, error) {
	totalQuantity, ok := checkedSum(existingQuantity, nextQuantity)
	if !ok || totalQuantity <= 0 {
		return 0, errors.New("paper weighted entry quantity overflow")
	}
	numerator := new(big.Int).Mul(big.NewInt(int64(existing)), big.NewInt(existingQuantity))
	numerator.Add(numerator, new(big.Int).Mul(big.NewInt(int64(next)), big.NewInt(nextQuantity)))
	quotient, remainder := new(big.Int), new(big.Int)
	quotient.QuoRem(numerator, big.NewInt(totalQuantity), remainder)
	if remainder.Sign() != 0 && side == domain.SideBuy {
		quotient.Add(quotient, big.NewInt(1))
	}
	if !quotient.IsInt64() || quotient.Sign() <= 0 {
		return 0, errors.New("paper weighted entry price overflow")
	}
	return domain.Price(quotient.Int64()), nil
}

func entryFillPrice(order domain.BrokerOrder, quote domain.Quote, config InstrumentConfig) (domain.Price, bool) {
	slip := domain.Price(config.SlippageTicks) * config.Instrument.TickSize
	switch order.Type {
	case domain.OrderTypeMarket:
		if order.Side == domain.SideBuy {
			return quote.Ask + slip, true
		}
		return quote.Bid - slip, true
	case domain.OrderTypeLimit:
		if order.Side == domain.SideBuy && quote.Ask <= order.EntryPrice {
			return order.EntryPrice, true
		}
		if order.Side == domain.SideSell && quote.Bid >= order.EntryPrice {
			return order.EntryPrice, true
		}
	case domain.OrderTypeStop:
		if order.Side == domain.SideBuy && quote.Ask >= order.EntryPrice {
			return maxPrice(order.EntryPrice, quote.Ask) + slip, true
		}
		if order.Side == domain.SideSell && quote.Bid <= order.EntryPrice {
			return minPrice(order.EntryPrice, quote.Bid) - slip, true
		}
	}
	return 0, false
}

func (b *Broker) tryCloseOnQuote(positionID string, quote domain.Quote) error {
	position := b.positions[positionID]
	config := b.config.Instruments[position.Symbol]
	slip := domain.Price(config.SlippageTicks) * config.Instrument.TickSize
	if position.Side == domain.SideBuy {
		if quote.Bid <= position.StopLoss {
			return b.closePosition(positionID, domain.FillStopLoss, quote.Bid-slip, quote.At)
		}
		if quote.Bid >= position.TakeProfit {
			return b.closePosition(positionID, domain.FillTakeProfit, position.TakeProfit, quote.At)
		}
		return nil
	}
	if quote.Ask >= position.StopLoss {
		return b.closePosition(positionID, domain.FillStopLoss, quote.Ask+slip, quote.At)
	}
	if quote.Ask <= position.TakeProfit {
		return b.closePosition(positionID, domain.FillTakeProfit, position.TakeProfit, quote.At)
	}
	return nil
}

// tryCloseOnCandle uses an adverse-first policy: when both protective levels
// are reachable in one OHLC candle and tick order is unknown, the stop is
// assumed to have filled first.
func (b *Broker) tryCloseOnCandle(positionID string, candle domain.Candle, halfSpreadTicks int64) error {
	position := b.positions[positionID]
	config := b.config.Instruments[position.Symbol]
	halfSpread := domain.Price(halfSpreadTicks) * config.Instrument.TickSize
	slip := domain.Price(config.SlippageTicks) * config.Instrument.TickSize
	if position.Side == domain.SideBuy {
		lowBid, highBid := candle.Low-halfSpread, candle.High-halfSpread
		if lowBid <= position.StopLoss {
			return b.closePosition(positionID, domain.FillStopLoss, minPrice(position.StopLoss, lowBid)-slip, candle.CloseTime)
		}
		if highBid >= position.TakeProfit {
			return b.closePosition(positionID, domain.FillTakeProfit, position.TakeProfit, candle.CloseTime)
		}
		return nil
	}
	lowAsk, highAsk := candle.Low+halfSpread, candle.High+halfSpread
	if highAsk >= position.StopLoss {
		return b.closePosition(positionID, domain.FillStopLoss, maxPrice(position.StopLoss, highAsk)+slip, candle.CloseTime)
	}
	if lowAsk <= position.TakeProfit {
		return b.closePosition(positionID, domain.FillTakeProfit, position.TakeProfit, candle.CloseTime)
	}
	return nil
}

func (b *Broker) closePosition(positionID string, role domain.FillRole, price domain.Price, at time.Time) error {
	position := b.positions[positionID]
	if price <= 0 {
		return errors.New("paper exit fill price is not positive")
	}
	config := b.config.Instruments[position.Symbol]
	commission, err := commissionFor(position.Quantity, config)
	if err != nil {
		return err
	}
	delta := int64(price - position.EntryPrice)
	if position.Side == domain.SideSell {
		delta = -delta
	}
	gross, err := config.PriceValue.PnLMinor(domain.Price(delta), position.Quantity, config.Instrument.QuantityStep)
	if err != nil {
		return fmt.Errorf("paper realized P&L: %w", err)
	}
	realizedNet, err := sumMinor(gross, -commission, position.AccruedRolloverMinor)
	if err != nil {
		return err
	}
	closedTradeNet, err := sumMinor(gross, -position.EntryCommissionMinor, -commission,
		position.AccruedRolloverMinor)
	if err != nil {
		return err
	}
	b.sequence++
	fill := domain.Fill{
		FillID: fmt.Sprintf("paper-fill-%06d", b.sequence), BrokerOrderID: position.BrokerOrderID,
		BrokerPositionID: positionID,
		IntentID:         position.IntentID, AccountID: position.AccountID, Symbol: position.Symbol,
		Side: opposite(position.Side), Role: role, Quantity: position.Quantity, Price: price,
		CommissionMinor: commission, RolloverMinor: position.AccruedRolloverMinor,
		RealizedGrossMinor:  gross,
		RealizedNetMinor:    realizedNet,
		ClosedTradeNetMinor: closedTradeNet,
		FilledAt:            at,
	}
	b.fills = append(b.fills, fill)
	if order, exists := b.orders[position.BrokerOrderID]; exists && order.Remaining > 0 {
		order.Status = domain.OrderCancelled
		order.UpdatedAt = at
		b.orders[position.BrokerOrderID] = order
	}
	delete(b.positions, positionID)
	return nil
}

func (b *Broker) accrueRollover(symbol string, through time.Time) error {
	config := b.config.Instruments[symbol]
	rollover := config.Rollover
	if !rollover.Enabled {
		return nil
	}
	through = through.UTC()
	for _, positionID := range b.positionIDs {
		position, exists := b.positions[positionID]
		if !exists || position.Symbol != symbol || !through.After(position.LastRolloverAt) {
			continue
		}
		last := position.LastRolloverAt.UTC()
		midnight := time.Date(last.Year(), last.Month(), last.Day(), 0, 0, 0, 0, time.UTC)
		boundary := midnight.Add(time.Duration(rollover.MinuteUTC) * time.Minute)
		if !boundary.After(last) {
			boundary = boundary.Add(24 * time.Hour)
		}
		for !boundary.After(through) {
			if rollover.ChargeWeekdays[boundary.Weekday()] {
				rate := rollover.LongPerQuantityStepMinor
				if position.Side == domain.SideSell {
					rate = rollover.ShortPerQuantityStepMinor
				}
				multiplier := int64(1)
				if boundary.Weekday() == rollover.TripleWeekday {
					multiplier = int64(rollover.TripleMultiplier)
				}
				charge, ok := checkedProduct(position.Quantity/config.Instrument.QuantityStep, rate, multiplier)
				if !ok {
					return errors.New("paper rollover charge overflow")
				}
				var err error
				position.AccruedRolloverMinor, err = addMinor(position.AccruedRolloverMinor, charge)
				if err != nil {
					return err
				}
			}
			boundary = boundary.Add(24 * time.Hour)
		}
		position.LastRolloverAt = through
		b.positions[positionID] = position
	}
	return nil
}

func commissionFor(quantity int64, config InstrumentConfig) (int64, error) {
	steps := quantity / config.Instrument.QuantityStep
	commission, ok := checkedProduct(steps, config.CommissionPerQuantityStepMinor, 1)
	if !ok {
		return 0, errors.New("paper commission overflow")
	}
	return commission, nil
}

func checkedProduct(a, b, c int64) (int64, bool) {
	value := new(big.Int).Mul(big.NewInt(a), big.NewInt(b))
	value.Mul(value, big.NewInt(c))
	return value.Int64(), value.IsInt64()
}

func checkedSum(left, right int64) (int64, bool) {
	value := new(big.Int).Add(big.NewInt(left), big.NewInt(right))
	return value.Int64(), value.IsInt64()
}

func addMinor(left, right int64) (int64, error) {
	value, ok := checkedSum(left, right)
	if !ok {
		return 0, errors.New("paper monetary amount overflow")
	}
	return value, nil
}

func sumMinor(values ...int64) (int64, error) {
	total := int64(0)
	for _, value := range values {
		var err error
		total, err = addMinor(total, value)
		if err != nil {
			return 0, err
		}
	}
	return total, nil
}

func copyFills(source []domain.Fill) []domain.Fill {
	result := make([]domain.Fill, len(source))
	copy(result, source)
	return result
}

func opposite(side domain.Side) domain.Side {
	if side == domain.SideBuy {
		return domain.SideSell
	}
	return domain.SideBuy
}

func sameApproval(a, b domain.ApprovedOrder) bool {
	if a.ApprovalID != b.ApprovalID || !a.ApprovedAt.Equal(b.ApprovedAt) {
		return false
	}
	ai, bi := a.Intent, b.Intent
	return ai.ID == bi.ID && ai.StrategyID == bi.StrategyID && ai.StrategyVersion == bi.StrategyVersion &&
		ai.AccountID == bi.AccountID && ai.Symbol == bi.Symbol && ai.Side == bi.Side && ai.Type == bi.Type &&
		ai.Quantity == bi.Quantity && ai.EntryPrice == bi.EntryPrice && ai.StopLoss == bi.StopLoss &&
		ai.TakeProfit == bi.TakeProfit && ai.SignalTime.Equal(bi.SignalTime) && ai.ExpiresAt.Equal(bi.ExpiresAt)
}

func minPrice(a, b domain.Price) domain.Price {
	if a < b {
		return a
	}
	return b
}

func maxPrice(a, b domain.Price) domain.Price {
	if a > b {
		return a
	}
	return b
}
