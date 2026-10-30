// Package signal provides the live signal engine that processes incoming
// candles through structure detection and outputs human-readable trade signals.
package signal

import (
	"fmt"
	"math"
	"strings"
	"time"

	"tradex/internal/domain"
	"tradex/internal/features"
	"tradex/internal/feed"
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
	Reason     string
	Timeframe  string
	Session    string
	Strategy   string
	Expires    time.Time
}

// Format returns a human-readable terminal output of the signal.
func (s Signal) Format() string {
	var b strings.Builder
	emoji := "🟢"
	if s.Direction == "SELL" {
		emoji = "🔴"
	}

	pipDiv := pipDivisor(s.Symbol)
	slPips := math.Abs(s.Entry-s.StopLoss) / pipDiv
	tpPips := math.Abs(s.TakeProfit-s.Entry) / pipDiv

	b.WriteString("\n═══════════════════════════════════════════════════\n")
	b.WriteString(fmt.Sprintf("%s SIGNAL: %s %s\n", emoji, s.Direction, s.Symbol))
	b.WriteString("───────────────────────────────────────────────────\n")
	b.WriteString(fmt.Sprintf("  Entry:       %.5f (market)\n", s.Entry))
	b.WriteString(fmt.Sprintf("  Stop Loss:   %.5f (%.1f pips)\n", s.StopLoss, slPips))
	b.WriteString(fmt.Sprintf("  Take Profit: %.5f (%.1f pips)\n", s.TakeProfit, tpPips))
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
	candles          []feed.Candle
	barsSeen         int
	triangle         *features.TrianglePattern
	triangleSignaled bool
}

// Engine processes candles and emits trade signals.
type Engine struct {
	config EngineConfig
	states map[string]*symbolState // key: "SYMBOL:INTERVAL"
	output chan Signal
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
	}, ch
}

// ProcessCandle ingests a candle and may emit a signal.
func (e *Engine) ProcessCandle(candle feed.Candle) {
	key := candle.Symbol + ":" + candle.Interval
	state := e.getOrCreateState(key, candle)
	if state == nil {
		return
	}

	state.candles = append(state.candles, candle)
	state.barsSeen++
	if len(state.candles) > 100 {
		state.candles = state.candles[len(state.candles)-100:]
	}

	// Convert to domain candle for swing detection
	domCandle := feedToDomainCandle(candle)
	if domCandle == nil {
		return
	}

	// Detect swings
	swings, err := state.detector.Push(*domCandle)
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
	breaks := state.structure.OnCandle(*domCandle, swings)
	for i := range breaks {
		brk := breaks[i]
		state.lastBreak = &brk
	}

	if e.config.EnableTriangle {
		if pattern, ok := features.DetectTriangle(state.swings, e.config.Triangle); ok {
			state.triangle = &pattern
		}
		if e.evaluateTriangleSignal(state, candle, *domCandle) {
			return
		}
	}

	// Generate signal from structure break + trend alignment
	e.evaluateSignal(state, candle)
}

func (e *Engine) getOrCreateState(key string, candle feed.Candle) *symbolState {
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

func (e *Engine) evaluateTriangleSignal(state *symbolState, candle feed.Candle, domCandle domain.Candle) bool {
	pattern := state.triangle
	if pattern == nil || state.triangleSignaled || !candle.Time.After(pattern.DetectedAt) {
		return false
	}

	direction, broke := pattern.Breakout(domCandle.Close, state.barsSeen-1, domain.Price(e.config.TriangleBreakoutBufferTicks))
	if !broke {
		return false
	}

	scale := priceScale(candle.Symbol)
	if scale <= 0 {
		return false
	}
	entry := candle.Close
	boundary := float64(pattern.LowerBoundaryAt(state.barsSeen-1)) / float64(scale)
	var stopLoss, takeProfit float64
	var signalDirection string
	switch direction {
	case features.BreakBullish:
		signalDirection = "BUY"
		stopLoss = math.Min(findRecentLow(state.candles), boundary)
		if stopLoss >= entry {
			return false
		}
		takeProfit = entry + (entry-stopLoss)*e.config.MinRewardRisk
	case features.BreakBearish:
		signalDirection = "SELL"
		boundary = float64(pattern.UpperBoundaryAt(state.barsSeen-1)) / float64(scale)
		stopLoss = math.Max(findRecentHigh(state.candles), boundary)
		if stopLoss <= entry {
			return false
		}
		takeProfit = entry - (stopLoss-entry)*e.config.MinRewardRisk
	default:
		return false
	}

	risk := math.Abs(entry - stopLoss)
	if risk <= 0 || math.Abs(takeProfit-entry)/risk < e.config.MinRewardRisk {
		return false
	}
	state.triangleSignaled = true
	select {
	case e.output <- Signal{
		Time:       candle.Time,
		Symbol:     candle.Symbol,
		Direction:  signalDirection,
		Entry:      entry,
		StopLoss:   stopLoss,
		TakeProfit: takeProfit,
		RiskReward: math.Abs(takeProfit-entry) / risk,
		Lots:       e.calculateLots(candle.Symbol, risk),
		Timeframe:  candle.Interval,
		Session:    detectSession(candle.Time),
		Strategy:   "Triangle Breakout: " + string(pattern.Kind),
		Reason:     fmt.Sprintf("confirmed %s close broke the projected triangle boundary", pattern.Kind),
		Expires:    candle.Time.Add(e.config.SignalExpiry),
	}:
	default:
	}
	return true
}

func (e *Engine) evaluateSignal(state *symbolState, candle feed.Candle) {
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
	breakAge := candle.Time.Sub(brk.KnownAt)
	maxAge := intervalDuration(candle.Interval) * 2
	if breakAge > maxAge || breakAge < 0 {
		return
	}

	var direction string
	var entry, sl, tp float64

	switch {
	case brk.Direction == features.BreakBullish && state.trend == features.TrendBullish:
		direction = "BUY"
		entry = candle.Close
		// Stop below the broken swing low or recent low
		sl = findRecentLow(state.candles)
		if sl >= entry {
			return // Invalid: SL above entry
		}
		risk := entry - sl
		tp = entry + risk*e.config.MinRewardRisk

	case brk.Direction == features.BreakBearish && state.trend == features.TrendBearish:
		direction = "SELL"
		entry = candle.Close
		// Stop above the broken swing high or recent high
		sl = findRecentHigh(state.candles)
		if sl <= entry {
			return // Invalid: SL below entry
		}
		risk := sl - entry
		tp = entry - risk*e.config.MinRewardRisk

	default:
		return // No alignment
	}

	risk := math.Abs(entry - sl)
	reward := math.Abs(tp - entry)
	if risk <= 0 {
		return
	}
	rr := reward / risk
	if rr < e.config.MinRewardRisk {
		return
	}

	lots := e.calculateLots(candle.Symbol, risk)

	signal := Signal{
		Time:       candle.Time,
		Symbol:     candle.Symbol,
		Direction:  direction,
		Entry:      entry,
		StopLoss:   sl,
		TakeProfit: tp,
		RiskReward: rr,
		Lots:       lots,
		Timeframe:  candle.Interval,
		Session:    detectSession(candle.Time),
		Strategy:   "Structure Break + Trend",
		Reason:     fmt.Sprintf("%s break aligned with %s trend", brk.Direction, state.trend),
		Expires:    candle.Time.Add(e.config.SignalExpiry),
	}

	// Clear the break so we don't signal again
	state.lastBreak = nil

	select {
	case e.output <- signal:
	default:
	}
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

func feedToDomainCandle(c feed.Candle) *domain.Candle {
	scale := priceScale(c.Symbol)
	tf := intervalDuration(c.Interval)
	if tf == 0 {
		return nil
	}
	openTime := c.Time.Add(-tf) // Candle time is close time in most feeds
	return &domain.Candle{
		Source:    "twelvedata",
		Symbol:    c.Symbol,
		Timeframe: tf,
		OpenTime:  openTime,
		CloseTime: c.Time,
		Open:      domain.Price(c.Open * scale),
		High:      domain.Price(c.High * scale),
		Low:       domain.Price(c.Low * scale),
		Close:     domain.Price(c.Close * scale),
		TickCount: max(uint64(c.Volume), 1),
	}
}

func intervalDuration(interval string) time.Duration {
	switch interval {
	case "1min":
		return time.Minute
	case "5min":
		return 5 * time.Minute
	case "15min":
		return 15 * time.Minute
	case "30min":
		return 30 * time.Minute
	case "45min":
		return 45 * time.Minute
	case "1h":
		return time.Hour
	case "2h":
		return 2 * time.Hour
	case "4h":
		return 4 * time.Hour
	case "8h":
		return 8 * time.Hour
	case "1day":
		return 24 * time.Hour
	default:
		return 0
	}
}

func priceScale(symbol string) float64 {
	if isJPYPair(symbol) {
		return 1000 // 3 decimal places
	}
	if isGold(symbol) || isIndex(symbol) {
		return 100 // 2 decimal places
	}
	return 100000 // 5 decimal places for most forex
}

func pipDivisor(symbol string) float64 {
	if isJPYPair(symbol) {
		return 0.01
	}
	if isGold(symbol) {
		return 0.1
	}
	if isSilver(symbol) {
		return 0.01
	}
	if isOil(symbol) || isIndex(symbol) {
		return 1.0
	}
	return 0.0001 // Standard forex pip
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

func findRecentLow(candles []feed.Candle) float64 {
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

func findRecentHigh(candles []feed.Candle) float64 {
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

func max(a, b uint64) uint64 {
	if a > b {
		return a
	}
	return b
}
