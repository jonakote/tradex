// Package signal provides the live signal engine that processes incoming
// candles through structure detection and outputs human-readable trade signals.
package signal

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"tradex/internal/domain"
	"tradex/internal/features"
	"tradex/internal/feed"
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
	// EnableCRT enables the deterministic Candle Range Theory research
	// strategy. It emits the armed setup only: entry is a resting limit, and
	// fill/stop/target adjudication requires a confirmation-timeframe stream
	// that this engine does not consume. See docs/CANDLE_RANGE_SPEC.md.
	EnableCRT bool                       `json:"enable_crt"`
	CRT       features.CandleRangeConfig `json:"crt"`
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
		EnableCRT:      false,
		CRT:            features.DefaultCandleRangeConfig(),
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
	// emitSuppressed is set while replaying stale backfill bars. Zero value
	// means "may emit", so a state built directly (replay, tests) behaves
	// exactly as before.
	emitSuppressed bool
	// previous is the last completed candle, held because a candle range is
	// a two-candle pattern.
	previous   *domain.Candle
	crtLastArm time.Time
}

// Engine processes candles and emits trade signals.
type Engine struct {
	config EngineConfig
	states map[string]*symbolState // key: "SYMBOL:INTERVAL"
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
	if cfg.EnableCRT && cfg.CRT.Validate() != nil {
		cfg.CRT = features.DefaultCandleRangeConfig()
	}
	ch := make(chan Signal, 32)
	return &Engine{
		config: cfg,
		states: make(map[string]*symbolState),
		output: ch,
		now:    time.Now,
	}, ch
}

// ProcessCandle ingests a candle and may emit a signal.
func (e *Engine) ProcessCandle(candle feed.Candle) {
	e.mu.Lock()
	defer e.mu.Unlock()

	key := candle.Symbol + ":" + candle.Interval
	state := e.getOrCreateState(key, candle)
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

	if e.config.EnableCRT {
		if e.evaluateCandleRangeSignal(state, candle, *domCandle) {
			state.previous = domCandle
			return
		}
	}
	state.previous = domCandle

	// Generate signal from structure break + trend alignment
	e.evaluateSignal(state, candle)
}

// evaluateCandleRangeSignal emits an armed Candle Range Theory setup. The
// pattern is only knowable at the purge candle's close, so this runs on
// completed candles exclusively, and the emitted entry is a resting limit at
// the purge candle's equilibrium rather than a market price.
//
// Only the ARMED state is reported. Fill, stop, target, and expiry
// adjudication needs a confirmation-timeframe stream, which this engine does
// not consume; that path lives in internal/research/crtlab.
func (e *Engine) evaluateCandleRangeSignal(state *symbolState, candle feed.Candle,
	domCandle domain.Candle) bool {

	if state.previous == nil {
		return false
	}
	config := e.config.CRT
	scale := priceScale(candle.Symbol)
	if scale <= 0 {
		return false
	}
	// A fixed tick floor is meaningless across instruments: derive one from
	// the symbol's price scale when the operator has not set it.
	if config.MinRangeTicks <= 0 {
		config.MinRangeTicks = int64(scale) / 1000
		if config.MinRangeTicks < 1 {
			config.MinRangeTicks = 1
		}
	}

	pattern, ok := features.DetectCandleRange(*state.previous, domCandle, config)
	if !ok {
		return false
	}
	// One signal per pattern, even if the feed replays the same closed bar.
	if !pattern.DetectedAt.After(state.crtLastArm) {
		return false
	}

	direction := "BUY"
	if pattern.Kind == features.CandleRangeBearish {
		direction = "SELL"
	}
	entry := float64(pattern.EntryReference) / scale
	stopLoss := float64(pattern.StopReference) / scale
	takeProfit := float64(pattern.TargetReference) / scale
	risk := math.Abs(entry - stopLoss)
	if risk <= 0 {
		return false
	}

	state.crtLastArm = pattern.DetectedAt
	// Stamp the instant the pattern became knowable: the bar's close.
	_, barClose := candle.Bar()
	return e.emit(state, Signal{
		Time:       barClose,
		Symbol:     candle.Symbol,
		Direction:  direction,
		Entry:      entry,
		StopLoss:   stopLoss,
		TakeProfit: takeProfit,
		RiskReward: float64(pattern.RewardRiskBps) / 10000,
		Lots:       e.calculateLots(candle.Symbol, risk),
		EntryType:  "LIMIT",
		Timeframe:  candle.Interval,
		Session:    detectSession(barClose),
		Strategy:   "Candle Range Theory (" + features.CandleRangeSpecVersion + "): " + string(pattern.Kind),
		Reason: fmt.Sprintf(
			"purge of %s by %d ticks (%s) closed back inside the range; limit rests at the purge equilibrium",
			strconv.FormatFloat(float64(pattern.SweptLevel)/scale, 'f', priceDigits(candle.Symbol), 64),
			pattern.PurgeTicks,
			strconv.FormatFloat(float64(pattern.PurgeTicks)/scale, 'f', priceDigits(candle.Symbol), 64)),
		// The spec expires an unfilled limit when the expansion candle
		// closes, which is one setup bar after detection.
		Expires: barClose.Add(intervalDuration(candle.Interval)),
	})
}

// isFresh reports whether a completed candle closed recently enough for its
// setup to still be actionable. Age is measured in the candle's own bars, so
// the same budget is sensible on 15m and on daily data.
func (e *Engine) isFresh(candle feed.Candle) bool {
	if e.config.MaxSignalAgeBars <= 0 {
		return true
	}
	interval := intervalDuration(candle.Interval)
	if interval <= 0 {
		return true
	}
	clock := e.now
	if clock == nil {
		clock = time.Now
	}
	// Age runs from the bar's close: that is when its pattern became
	// knowable. Measuring from the open overstates age by a full bar and
	// suppresses genuinely fresh signals.
	_, closeTime := candle.Bar()
	if closeTime.IsZero() {
		return true
	}
	return clock().UTC().Sub(closeTime) <=
		time.Duration(e.config.MaxSignalAgeBars)*interval
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
	_, barClose := candle.Bar()
	return e.emit(state, Signal{
		Time:       barClose,
		Symbol:     candle.Symbol,
		Direction:  signalDirection,
		Entry:      entry,
		StopLoss:   stopLoss,
		TakeProfit: takeProfit,
		RiskReward: math.Abs(takeProfit-entry) / risk,
		Lots:       e.calculateLots(candle.Symbol, risk),
		Timeframe:  candle.Interval,
		Session:    detectSession(barClose),
		Strategy:   "Triangle Breakout: " + string(pattern.Kind),
		Reason:     fmt.Sprintf("confirmed %s close broke the projected triangle boundary", pattern.Kind),
		Expires:    barClose.Add(e.config.SignalExpiry),
	})
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

	_, barClose := candle.Bar()
	signal := Signal{
		Time:       barClose,
		Symbol:     candle.Symbol,
		Direction:  direction,
		Entry:      entry,
		StopLoss:   sl,
		TakeProfit: tp,
		RiskReward: rr,
		Lots:       lots,
		Timeframe:  candle.Interval,
		Session:    detectSession(barClose),
		Strategy:   "Structure Break + Trend",
		Reason:     fmt.Sprintf("%s break aligned with %s trend", brk.Direction, state.trend),
		Expires:    barClose.Add(e.config.SignalExpiry),
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

func feedToDomainCandle(c feed.Candle) *domain.Candle {
	tf := intervalDuration(c.Interval)
	if tf == 0 {
		return nil
	}
	// Use the boundaries the producer declared. Inferring them from a single
	// timestamp requires guessing whether the vendor stamps a bar by its
	// open or its close, and guessing wrong shifts every candle by a bar.
	openTime, closeTime := c.Bar()
	if openTime.IsZero() || closeTime.IsZero() {
		return nil
	}
	// Fail closed on an unverified instrument: processing it would require
	// assuming a tick size, and every downstream tick parameter would then
	// mean something different from what the operator configured.
	spec, ok := instrument.Lookup(c.Symbol)
	if !ok {
		return nil
	}
	return &domain.Candle{
		Source:    "feed",
		Symbol:    c.Symbol,
		Timeframe: tf,
		OpenTime:  openTime,
		CloseTime: closeTime,
		Open:      domain.Price(spec.Ticks(c.Open)),
		High:      domain.Price(spec.Ticks(c.High)),
		Low:       domain.Price(spec.Ticks(c.Low)),
		Close:     domain.Price(spec.Ticks(c.Close)),
		TickCount: max(uint64(c.Volume), 1),
	}
}

// intervalDuration delegates to the shared feed table, which covers every
// vendor spelling. A local copy previously knew only the Twelve Data forms,
// so a Binance "15m" or "1d" stream resolved to zero and every candle was
// silently dropped.
func intervalDuration(interval string) time.Duration {
	return feed.IntervalDuration(interval)
}

// priceScale returns the verified ticks-per-unit for a symbol, or 0 when the
// instrument is not in the verified registry. Callers must treat 0 as "do not
// process": a guessed scale silently changes the meaning of every
// tick-denominated parameter.
func priceScale(symbol string) float64 {
	spec, ok := instrument.Lookup(symbol)
	if !ok {
		return 0
	}
	return float64(spec.PriceScale)
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
