// Package signal provides the live signal engine that processes incoming
// candles through structure detection and outputs human-readable trade signals.
package signal

import (
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"tradex/internal/domain"
	"tradex/internal/features"
	"tradex/internal/instrument"
)

// Signal represents a trade signal ready for manual execution.
type Signal struct {
	Time       time.Time
	Symbol     string
	Direction  string // "BUY" or "SELL"
	Entry      float64
	StopLoss   float64
	TakeProfit float64
	RiskReward float64
	Lots       float64
	// EntryType is "LIMIT" when the strategy declares a resting price, and
	// "MARKET" (or empty) when the signal is actionable at the close. CRT is
	// a limit model: entering at market would change the trade.
	EntryType string
	Reason    string
	Timeframe string
	Session   string
	Strategy  string
	Expires   time.Time
}

// Format returns a human-readable terminal output of the signal.
func (s Signal) Format() string {
	var b strings.Builder
	emoji := "🟢"
	if s.Direction == "SELL" {
		emoji = "🔴"
	}

	// Quoting a crypto distance "in pips" produces numbers in the millions,
	// which is worse than saying nothing. Report absolute price distance for
	// instruments that have no pip convention.
	slDistance := math.Abs(s.Entry - s.StopLoss)
	tpDistance := math.Abs(s.TakeProfit - s.Entry)
	unit := "pips"
	if pipDiv := pipDivisor(s.Symbol); pipDiv > 0 {
		slDistance /= pipDiv
		tpDistance /= pipDiv
	} else {
		unit = "price"
	}
	// Print at the instrument's own precision: %.5f on BTCUSDT invents three
	// digits the venue does not quote.
	digits := priceDigits(s.Symbol)
	priceFmt := fmt.Sprintf("%%.%df", digits)

	b.WriteString("\n═══════════════════════════════════════════════════\n")
	b.WriteString(fmt.Sprintf("%s SIGNAL: %s %s\n", emoji, s.Direction, s.Symbol))
	b.WriteString("───────────────────────────────────────────────────\n")
	entryKind := "market"
	if strings.EqualFold(s.EntryType, "LIMIT") {
		entryKind = "LIMIT — do not enter at market"
	}
	b.WriteString(fmt.Sprintf("  Entry:       "+priceFmt+" (%s)\n", s.Entry, entryKind))
	b.WriteString(fmt.Sprintf("  Stop Loss:   "+priceFmt+" (%.1f %s)\n", s.StopLoss, slDistance, unit))
	b.WriteString(fmt.Sprintf("  Take Profit: "+priceFmt+" (%.1f %s)\n", s.TakeProfit, tpDistance, unit))
	b.WriteString(fmt.Sprintf("  R:R:         %.1f\n", s.RiskReward))
	if s.Lots > 0 {
		b.WriteString(fmt.Sprintf("  Size:        %.2f lots\n", s.Lots))
	}
	b.WriteString(fmt.Sprintf("  Timeframe:   %s\n", s.Timeframe))
	if s.Session != "" {
		b.WriteString(fmt.Sprintf("  Session:     %s\n", s.Session))
	}
	b.WriteString(fmt.Sprintf("  Strategy:    %s\n", s.Strategy))
	b.WriteString(fmt.Sprintf("  Reason:      %s\n", s.Reason))
	b.WriteString(fmt.Sprintf("  Signal Time: %s\n", s.Time.Format("2006-01-02 15:04 UTC")))
	if !s.Expires.IsZero() {
		b.WriteString(fmt.Sprintf("  Expires:     %s\n", s.Expires.Format("2006-01-02 15:04 UTC")))
	}
	b.WriteString("═══════════════════════════════════════════════════\n")
	return b.String()
}

// EngineConfig configures the signal engine.
type EngineConfig struct {
	// Risk per trade as fraction of equity (e.g. 0.0025 = 0.25%)
	RiskFraction float64 `json:"risk_fraction"`
	// Account equity for lot sizing
	AccountEquity float64 `json:"account_equity"`
	// Minimum reward:risk ratio to emit a signal
	MinRewardRisk float64 `json:"min_reward_risk"`
	// Swing detection left/right window
	SwingLeft  int `json:"swing_left"`
	SwingRight int `json:"swing_right"`
	// Signal expiry duration
	SignalExpiry time.Duration `json:"signal_expiry"`
	// Verbose enables per-candle debug logging (structure breaks, trend state)
	Verbose bool `json:"verbose"`
	// EnableTriangle enables the deterministic triangle-breakout research strategy.
	EnableTriangle bool                    `json:"enable_triangle"`
	Triangle       features.TriangleConfig `json:"triangle"`
	// TriangleBreakoutBufferTicks is an additional close-through buffer in price ticks.
	TriangleBreakoutBufferTicks int64 `json:"triangle_breakout_buffer_ticks"`
	// MaxSignalAgeBars suppresses signals from candles that closed more than
	// this many bars ago. A poller that backfills history would otherwise
	// emit a signal for every historical pattern, flooding a live monitor
	// with trades whose windows closed weeks earlier. Zero disables the
	// check, which is correct for replay and for tests.
	MaxSignalAgeBars int `json:"max_signal_age_bars"`
}

// DefaultEngineConfig returns sensible defaults.
func DefaultEngineConfig() EngineConfig {
	return EngineConfig{
		RiskFraction:   0.0025,
		AccountEquity:  10000.0,
		MinRewardRisk:  2.0,
		SwingLeft:      3,
		SwingRight:     2,
		SignalExpiry:   4 * time.Hour,
		EnableTriangle: true,
		Triangle:       features.DefaultTriangleConfig(),
	}
}

// symbolState tracks the analysis state for a single symbol/timeframe pair.
type symbolState struct {
	detector         *features.SwingDetector
	structure        *features.StructureDetector
	swings           []features.Swing
	trend            features.Trend
	lastBreak        *features.StructureBreak
	candles          []domain.Candle
	barsSeen         int
	triangle         *features.TrianglePattern
	triangleSignaled bool
	// emitSuppressed is set while replaying stale backfill bars. Zero value
	// means "may emit", so a state built directly (replay, tests) behaves
	// exactly as before.
	emitSuppressed bool
}

// Engine processes candles and emits trade signals.
type Engine struct {
	config EngineConfig
	states map[string]*symbolState // key: "SYMBOL:TIMEFRAME"
	output chan Signal
	// Several feed pollers call ProcessCandle from their own goroutines, so
	// the state map needs guarding.
	mu sync.Mutex
	// now is injectable so staleness is testable without sleeping.
	now func() time.Time
}

// NewEngine creates a signal engine.
func NewEngine(cfg EngineConfig) (*Engine, chan Signal) {
	if cfg.EnableTriangle && cfg.Triangle.MinTouches == 0 {
		cfg.Triangle = features.DefaultTriangleConfig()
	}
	if cfg.TriangleBreakoutBufferTicks < 0 {
		cfg.TriangleBreakoutBufferTicks = 0
	}
	ch := make(chan Signal, 32)
	return &Engine{
		config: cfg,
		states: make(map[string]*symbolState),
		output: ch,
		now:    time.Now,
	}, ch
}

// ProcessCandle ingests one closed candle and may emit a signal.
//
// It takes domain.Candle directly: internal/feed yields exactly one candle
// type with integer-tick prices, and every price-action computation below
// (swings, structure, triangle geometry, entry/stop/target) stays in those
// ticks. A float64 price appears only in the final Signal, which exists
// purely for human display -- this is the single conversion point, matching
// the "one candle type, converted once, at the boundary" rule.
func (e *Engine) ProcessCandle(candle domain.Candle) {
	e.mu.Lock()
	defer e.mu.Unlock()

	key := candle.Symbol + ":" + timeframeLabel(candle.Timeframe)
	state := e.getOrCreateState(key)
	if state == nil {
		return
	}

	// Warm-up bars still build structure, swings and trend; they just must
	// not emit. This is what lets a backfilled poller reach a correct live
	// state without announcing stale setups.
	state.emitSuppressed = !e.isFresh(candle)

	state.candles = append(state.candles, candle)
	state.barsSeen++
	if len(state.candles) > 100 {
		state.candles = state.candles[len(state.candles)-100:]
	}

	// Detect swings
	swings, err := state.detector.Push(candle)
	if err != nil {
		return
	}
	state.swings = append(state.swings, swings...)
	if len(state.swings) > 50 {
		state.swings = state.swings[len(state.swings)-50:]
	}

	// Classify trend
	state.trend = features.ClassifyTrend(state.swings)

	// Detect structure breaks
	breaks := state.structure.OnCandle(candle, swings)
	for i := range breaks {
		brk := breaks[i]
		state.lastBreak = &brk
	}

	if e.config.EnableTriangle {
		if pattern, ok := features.DetectTriangle(state.swings, e.config.Triangle); ok {
			state.triangle = &pattern
		}
		if e.evaluateTriangleSignal(state, candle) {
			return
		}
	}

	// Generate signal from structure break + trend alignment
	e.evaluateSignal(state, candle)
}

// isFresh reports whether a completed candle closed recently enough for its
// setup to still be actionable. Age is measured in the candle's own bars, so
// the same budget is sensible on 15m and on daily data.
func (e *Engine) isFresh(candle domain.Candle) bool {
	if e.config.MaxSignalAgeBars <= 0 {
		return true
	}
	if candle.Timeframe <= 0 {
		return true
	}
	clock := e.now
	if clock == nil {
		clock = time.Now
	}
	// Age runs from the bar's close: that is when its pattern became
	// knowable. Measuring from the open overstates age by a full bar and
	// suppresses genuinely fresh signals.
	if candle.CloseTime.IsZero() {
		return true
	}
	return clock().UTC().Sub(candle.CloseTime) <=
		time.Duration(e.config.MaxSignalAgeBars)*candle.Timeframe
}

// emit publishes a signal unless the originating candle is stale. It never
// blocks: a full channel drops the signal rather than stalling a feed poller.
func (e *Engine) emit(state *symbolState, signal Signal) bool {
	if state.emitSuppressed {
		return false
	}
	select {
	case e.output <- signal:
	default:
	}
	return true
}

func (e *Engine) getOrCreateState(key string) *symbolState {
	if state, exists := e.states[key]; exists {
		return state
	}
	detector, err := features.NewSwingDetector(e.config.SwingLeft, e.config.SwingRight)
	if err != nil {
		return nil
	}
	state := &symbolState{
		detector:  detector,
		structure: &features.StructureDetector{},
	}
	e.states[key] = state
	return state
}

func (e *Engine) evaluateTriangleSignal(state *symbolState, candle domain.Candle) bool {
	pattern := state.triangle
	if pattern == nil || state.triangleSignaled || !candle.CloseTime.After(pattern.DetectedAt) {
		return false
	}

	direction, broke := pattern.Breakout(candle.Close, state.barsSeen-1, domain.Price(e.config.TriangleBreakoutBufferTicks))
	if !broke {
		return false
	}

	spec, ok := instrument.Lookup(candle.Symbol)
	if !ok {
		return false
	}
	entryTicks := candle.Close
	boundaryTicks := pattern.LowerBoundaryAt(state.barsSeen - 1)
	var stopTicks, targetTicks domain.Price
	var signalDirection string
	switch direction {
	case features.BreakBullish:
		signalDirection = "BUY"
		stopTicks = minPrice(findRecentLow(state.candles), boundaryTicks)
		if stopTicks >= entryTicks {
			return false
		}
		riskTicks := entryTicks - stopTicks
		targetTicks = entryTicks + scaleRiskTicks(riskTicks, e.config.MinRewardRisk)
	case features.BreakBearish:
		signalDirection = "SELL"
		boundaryTicks = pattern.UpperBoundaryAt(state.barsSeen - 1)
		stopTicks = maxPrice(findRecentHigh(state.candles), boundaryTicks)
		if stopTicks <= entryTicks {
			return false
		}
		riskTicks := stopTicks - entryTicks
		targetTicks = entryTicks - scaleRiskTicks(riskTicks, e.config.MinRewardRisk)
	default:
		return false
	}

	riskTicks := absPrice(entryTicks - stopTicks)
	rewardTicks := absPrice(targetTicks - entryTicks)
	if riskTicks <= 0 || float64(rewardTicks)/float64(riskTicks) < e.config.MinRewardRisk {
		return false
	}
	state.triangleSignaled = true
	return e.emit(state, Signal{
		Time:       candle.CloseTime,
		Symbol:     candle.Symbol,
		Direction:  signalDirection,
		Entry:      spec.Price(int64(entryTicks)),
		StopLoss:   spec.Price(int64(stopTicks)),
		TakeProfit: spec.Price(int64(targetTicks)),
		RiskReward: float64(rewardTicks) / float64(riskTicks),
		Lots:       e.calculateLots(candle.Symbol, spec.Price(int64(riskTicks))),
		Timeframe:  timeframeLabel(candle.Timeframe),
		Session:    detectSession(candle.CloseTime),
		Strategy:   "Triangle Breakout: " + string(pattern.Kind),
		Reason:     fmt.Sprintf("confirmed %s close broke the projected triangle boundary", pattern.Kind),
		Expires:    candle.CloseTime.Add(e.config.SignalExpiry),
	})
}

func (e *Engine) evaluateSignal(state *symbolState, candle domain.Candle) {
	if state.lastBreak == nil || state.trend == features.TrendNeutral {
		return
	}
	if e.config.Verbose {
		fmt.Printf("DEBUG: lastBreak=%+v trend=%s candles=%d\n", state.lastBreak, state.trend, len(state.candles))
	}

	// Only signal when break direction aligns with trend
	brk := state.lastBreak

	// Check that the break just happened (within last 2 candles)
	if len(state.candles) < 2 {
		return
	}
	breakAge := candle.CloseTime.Sub(brk.KnownAt)
	maxAge := candle.Timeframe * 2
	if breakAge > maxAge || breakAge < 0 {
		return
	}

	spec, ok := instrument.Lookup(candle.Symbol)
	if !ok {
		return
	}

	var direction string
	var entryTicks, slTicks, tpTicks domain.Price

	switch {
	case brk.Direction == features.BreakBullish && state.trend == features.TrendBullish:
		direction = "BUY"
		entryTicks = candle.Close
		// Stop below the broken swing low or recent low
		slTicks = findRecentLow(state.candles)
		if slTicks >= entryTicks {
			return // Invalid: SL above entry
		}
		riskTicks := entryTicks - slTicks
		tpTicks = entryTicks + scaleRiskTicks(riskTicks, e.config.MinRewardRisk)

	case brk.Direction == features.BreakBearish && state.trend == features.TrendBearish:
		direction = "SELL"
		entryTicks = candle.Close
		// Stop above the broken swing high or recent high
		slTicks = findRecentHigh(state.candles)
		if slTicks <= entryTicks {
			return // Invalid: SL below entry
		}
		riskTicks := slTicks - entryTicks
		tpTicks = entryTicks - scaleRiskTicks(riskTicks, e.config.MinRewardRisk)

	default:
		return // No alignment
	}

	riskTicks := absPrice(entryTicks - slTicks)
	rewardTicks := absPrice(tpTicks - entryTicks)
	if riskTicks <= 0 {
		return
	}
	rr := float64(rewardTicks) / float64(riskTicks)
	if rr < e.config.MinRewardRisk {
		return
	}

	lots := e.calculateLots(candle.Symbol, spec.Price(int64(riskTicks)))

	signal := Signal{
		Time:       candle.CloseTime,
		Symbol:     candle.Symbol,
		Direction:  direction,
		Entry:      spec.Price(int64(entryTicks)),
		StopLoss:   spec.Price(int64(slTicks)),
		TakeProfit: spec.Price(int64(tpTicks)),
		RiskReward: rr,
		Lots:       lots,
		Timeframe:  timeframeLabel(candle.Timeframe),
		Session:    detectSession(candle.CloseTime),
		Strategy:   "Structure Break + Trend",
		Reason:     fmt.Sprintf("%s break aligned with %s trend", brk.Direction, state.trend),
		Expires:    candle.CloseTime.Add(e.config.SignalExpiry),
	}

	// Clear the break so we don't signal again
	state.lastBreak = nil

	e.emit(state, signal)
}

func (e *Engine) calculateLots(symbol string, riskInPrice float64) float64 {
	if e.config.AccountEquity <= 0 || e.config.RiskFraction <= 0 || riskInPrice <= 0 {
		return 0
	}
	riskAmount := e.config.AccountEquity * e.config.RiskFraction
	pipValue := pipDivisor(symbol)
	riskPips := riskInPrice / pipValue
	if riskPips <= 0 {
		return 0
	}
	// Standard lot = $10/pip for most forex, approximate
	lotPipValue := 10.0
	if isJPYPair(symbol) {
		lotPipValue = 10.0 // Approximately $10/pip for JPY pairs at standard lot
	} else if isGold(symbol) {
		lotPipValue = 10.0 // $10 per pip (0.1) for 1 lot XAU/USD
	} else if isIndex(symbol) {
		lotPipValue = 1.0 // Varies significantly by index
	}
	lots := riskAmount / (riskPips * lotPipValue)
	// Round down to 0.01
	lots = math.Floor(lots*100) / 100
	if lots < 0.01 {
		lots = 0.01
	}
	return lots
}

// ---------- Helpers ----------

// scaleRiskTicks scales a risk distance in ticks by a reward:risk ratio,
// staying in integer ticks (rounded to the nearest tick) rather than
// round-tripping through a price float.
func scaleRiskTicks(riskTicks domain.Price, ratio float64) domain.Price {
	return domain.Price(math.Round(float64(riskTicks) * ratio))
}

func absPrice(value domain.Price) domain.Price {
	if value < 0 {
		return -value
	}
	return value
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

// timeframeLabel renders a candle timeframe for display and for the
// per-symbol state key. It does not need to match any vendor's interval
// spelling -- it only needs to be stable and human-readable.
func timeframeLabel(d time.Duration) string {
	switch {
	case d <= 0:
		return "0"
	case d < time.Minute:
		return d.String()
	case d%(24*time.Hour) == 0:
		return fmt.Sprintf("%dd", d/(24*time.Hour))
	case d%time.Hour == 0:
		return fmt.Sprintf("%dh", d/time.Hour)
	default:
		return fmt.Sprintf("%dm", d/time.Minute)
	}
}

// priceDigits returns the quote precision for display, defaulting to 5 for
// unverified symbols so formatting never loses information.
func priceDigits(symbol string) int {
	if spec, ok := instrument.Lookup(symbol); ok {
		return spec.Digits
	}
	return 5
}

func pipDivisor(symbol string) float64 {
	if spec, ok := instrument.Lookup(symbol); ok {
		return spec.PipSize
	}
	return 0
}

func isJPYPair(symbol string) bool {
	return strings.Contains(symbol, "JPY")
}

func isGold(symbol string) bool {
	return strings.Contains(symbol, "XAU")
}

func isSilver(symbol string) bool {
	return strings.Contains(symbol, "XAG")
}

func isOil(symbol string) bool {
	return strings.Contains(symbol, "BRENT") || strings.Contains(symbol, "WTI") || strings.Contains(symbol, "OIL")
}

func isIndex(symbol string) bool {
	return strings.Contains(symbol, "NQ") || strings.Contains(symbol, "IXIC") ||
		strings.Contains(symbol, "SPX") || strings.Contains(symbol, "US500") ||
		strings.Contains(symbol, "US100") || strings.Contains(symbol, "NAS")
}

func findRecentLow(candles []domain.Candle) domain.Price {
	if len(candles) == 0 {
		return 0
	}
	lookback := 10
	if len(candles) < lookback {
		lookback = len(candles)
	}
	low := candles[len(candles)-1].Low
	for i := len(candles) - lookback; i < len(candles); i++ {
		if candles[i].Low < low {
			low = candles[i].Low
		}
	}
	return low
}

func findRecentHigh(candles []domain.Candle) domain.Price {
	if len(candles) == 0 {
		return 0
	}
	lookback := 10
	if len(candles) < lookback {
		lookback = len(candles)
	}
	high := candles[len(candles)-1].High
	for i := len(candles) - lookback; i < len(candles); i++ {
		if candles[i].High > high {
			high = candles[i].High
		}
	}
	return high
}

func detectSession(t time.Time) string {
	hour := t.UTC().Hour()
	switch {
	case hour >= 22 || hour < 6:
		return "Sydney/Asia"
	case hour >= 0 && hour < 8:
		return "Tokyo"
	case hour >= 7 && hour < 16:
		if hour >= 12 && hour < 16 {
			return "London–New York overlap"
		}
		return "London"
	case hour >= 12 && hour < 21:
		return "New York"
	default:
		return ""
	}
}
