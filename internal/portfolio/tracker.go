package portfolio

import (
	"errors"
	"fmt"
	"math"
	"math/big"
	"sync"
	"time"

	"tradex/internal/domain"
)

type View struct {
	EquityMinor            int64
	AggregateOpenRiskMinor int64
	DailyPnLMinor          int64
	WeeklyPnLMinor         int64
	DrawdownBPS            int64
	ConsecutiveLosses      int
	OpenPositions          int
	SymbolOpenPositions    int
	BucketOpenRiskMinor    map[string]int64
	DirectionalRiskMinor   map[string]int64
}

type reservation struct {
	instrument domain.Instrument
	side       domain.Side
	risk       int64
}

type Tracker struct {
	mu                sync.Mutex
	equity            int64
	peak              int64
	dailyPnL          int64
	weeklyPnL         int64
	day               string
	week              string
	consecutiveLosses int
	reservations      map[string]reservation
	aggregateRisk     int64
	bucketRisk        map[string]int64
	directionalRisk   map[string]int64
}

func New(initialEquityMinor int64) (*Tracker, error) {
	if initialEquityMinor <= 0 {
		return nil, errors.New("initial portfolio equity must be positive")
	}
	return &Tracker{
		equity: initialEquityMinor, peak: initialEquityMinor, reservations: make(map[string]reservation),
		bucketRisk: make(map[string]int64), directionalRisk: make(map[string]int64),
	}, nil
}

func (t *Tracker) View(at time.Time, symbol string) (View, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.advance(at); err != nil {
		return View{}, err
	}
	symbolPositions := 0
	for _, open := range t.reservations {
		if open.instrument.Symbol == symbol {
			symbolPositions++
		}
	}
	drawdown := int64(0)
	if t.equity < t.peak {
		drawdown = drawdownBPS(t.peak, t.equity)
	}
	return View{
		EquityMinor: t.equity, AggregateOpenRiskMinor: t.aggregateRisk,
		DailyPnLMinor: t.dailyPnL, WeeklyPnLMinor: t.weeklyPnL, DrawdownBPS: drawdown,
		ConsecutiveLosses: t.consecutiveLosses, OpenPositions: len(t.reservations),
		SymbolOpenPositions: symbolPositions,
		BucketOpenRiskMinor: copyMap(t.bucketRisk), DirectionalRiskMinor: copyMap(t.directionalRisk),
	}, nil
}

// Reserve records worst-case risk as soon as an order is approved, covering
// both acknowledged orders and open positions until a closing fill arrives.
func (t *Tracker) Reserve(intentID string, instrument domain.Instrument, side domain.Side, riskMinor int64) error {
	if intentID == "" || riskMinor <= 0 || !side.Valid() {
		return errors.New("portfolio reservation identity and risk are required")
	}
	if err := instrument.Validate(); err != nil {
		return err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, exists := t.reservations[intentID]; exists {
		return errors.New("portfolio intent risk is already reserved")
	}
	if t.aggregateRisk > math.MaxInt64-riskMinor {
		return errors.New("portfolio aggregate risk overflow")
	}
	for _, bucket := range []string{instrument.BaseCurrency, instrument.QuoteCurrency} {
		if t.bucketRisk[bucket] > math.MaxInt64-riskMinor {
			return errors.New("portfolio exposure bucket overflow")
		}
	}
	baseDirection := riskMinor
	if side == domain.SideSell {
		baseDirection = -riskMinor
	}
	quoteDirection := -baseDirection
	if _, ok := safeAdd(t.directionalRisk[instrument.BaseCurrency], baseDirection); !ok {
		return errors.New("portfolio base directional exposure overflow")
	}
	if _, ok := safeAdd(t.directionalRisk[instrument.QuoteCurrency], quoteDirection); !ok {
		return errors.New("portfolio quote directional exposure overflow")
	}
	t.reservations[intentID] = reservation{instrument: instrument, side: side, risk: riskMinor}
	t.aggregateRisk += riskMinor
	t.bucketRisk[instrument.BaseCurrency] += riskMinor
	t.bucketRisk[instrument.QuoteCurrency] += riskMinor
	t.directionalRisk[instrument.BaseCurrency] += baseDirection
	t.directionalRisk[instrument.QuoteCurrency] += quoteDirection
	return nil
}

func (t *Tracker) ApplyClosingFill(fill domain.Fill) error {
	if fill.Role != domain.FillStopLoss && fill.Role != domain.FillTakeProfit {
		return errors.New("portfolio requires a closing fill")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.advance(fill.FilledAt); err != nil {
		return err
	}
	open, exists := t.reservations[fill.IntentID]
	if !exists || open.instrument.Symbol != fill.Symbol {
		return errors.New("closing fill has no matching portfolio reservation")
	}
	nextEquity, ok := safeAdd(t.equity, fill.ClosedTradeNetMinor)
	if !ok {
		return errors.New("portfolio equity overflow")
	}
	nextDaily, ok := safeAdd(t.dailyPnL, fill.ClosedTradeNetMinor)
	if !ok {
		return errors.New("portfolio daily P&L overflow")
	}
	nextWeekly, ok := safeAdd(t.weeklyPnL, fill.ClosedTradeNetMinor)
	if !ok {
		return errors.New("portfolio weekly P&L overflow")
	}
	t.equity, t.dailyPnL, t.weeklyPnL = nextEquity, nextDaily, nextWeekly
	if t.equity > t.peak {
		t.peak = t.equity
	}
	if fill.ClosedTradeNetMinor < 0 {
		t.consecutiveLosses++
	} else {
		t.consecutiveLosses = 0
	}
	t.release(fill.IntentID, open)
	return nil
}

func (t *Tracker) Release(intentID string) error {
	if intentID == "" {
		return errors.New("portfolio release intent ID is required")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	open, exists := t.reservations[intentID]
	if !exists {
		return errors.New("portfolio reservation not found")
	}
	t.release(intentID, open)
	return nil
}

func (t *Tracker) release(intentID string, open reservation) {
	t.aggregateRisk -= open.risk
	baseDirection := open.risk
	if open.side == domain.SideSell {
		baseDirection = -open.risk
	}
	t.bucketRisk[open.instrument.BaseCurrency] -= open.risk
	t.bucketRisk[open.instrument.QuoteCurrency] -= open.risk
	t.directionalRisk[open.instrument.BaseCurrency] -= baseDirection
	t.directionalRisk[open.instrument.QuoteCurrency] += baseDirection
	for _, bucket := range []string{open.instrument.BaseCurrency, open.instrument.QuoteCurrency} {
		if t.bucketRisk[bucket] == 0 {
			delete(t.bucketRisk, bucket)
		}
		if t.directionalRisk[bucket] == 0 {
			delete(t.directionalRisk, bucket)
		}
	}
	delete(t.reservations, intentID)
}

func (t *Tracker) advance(at time.Time) error {
	if at.IsZero() {
		return errors.New("portfolio time is required")
	}
	utc := at.UTC()
	day := utc.Format("2006-01-02")
	year, weekNumber := utc.ISOWeek()
	week := fmt.Sprintf("%04d/%02d", year, weekNumber)
	if t.day != "" && day != t.day {
		t.dailyPnL = 0
	}
	if t.week != "" && week != t.week {
		t.weeklyPnL = 0
	}
	t.day, t.week = day, week
	return nil
}

func drawdownBPS(peak, equity int64) int64 {
	if peak <= 0 || equity >= peak {
		return 0
	}
	numerator := new(big.Int).Sub(big.NewInt(peak), big.NewInt(equity))
	numerator.Mul(numerator, big.NewInt(10000))
	numerator.Quo(numerator, big.NewInt(peak))
	if !numerator.IsInt64() {
		return math.MaxInt64
	}
	return numerator.Int64()
}

func safeAdd(a, b int64) (int64, bool) {
	if b > 0 && a > math.MaxInt64-b || b < 0 && a < math.MinInt64-b {
		return 0, false
	}
	return a + b, true
}

func copyMap(source map[string]int64) map[string]int64 {
	result := make(map[string]int64, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}
