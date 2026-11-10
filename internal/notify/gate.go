package notify

import (
	"errors"
	"sort"
	"strings"
	"sync"
	"time"
)

// Gate decides whether a detected setup may be delivered.
//
// These are the filters that have nothing to do with whether the pattern is
// present, and everything to do with whether acting on it is sane. They are
// deliberately separate from the detector so a research run can measure the
// pattern with and without them.
type Gate struct {
	// Blackouts suppress delivery around scheduled high-impact releases. A
	// limit order with a 10-pip stop held through non-farm payrolls is not
	// a stop, it is a coin toss with slippage.
	Blackouts []Blackout
	// Groups caps concurrent signals on correlated instruments. EURUSD and
	// AUDUSD short at the same hour is one bet on the dollar, sized twice.
	Groups map[string]string
	// GroupCooldown is how long a group stays occupied after a signal.
	GroupCooldown time.Duration
	// SymbolCooldown suppresses repeat signals on one instrument.
	SymbolCooldown time.Duration

	mu          sync.Mutex
	lastByGroup map[string]time.Time
	lastBySym   map[string]time.Time
	lastDetect  map[string]time.Time
}

// Blackout is a window around a scheduled event, in UTC.
type Blackout struct {
	Name string `json:"name"`
	// At is the release instant, UTC.
	At time.Time `json:"at"`
	// Before and After bound the suppressed window around At.
	Before time.Duration `json:"before"`
	After  time.Duration `json:"after"`
}

func (b Blackout) covers(at time.Time) bool {
	return !at.Before(b.At.Add(-b.Before)) && !at.After(b.At.Add(b.After))
}

func (b Blackout) Validate() error {
	if strings.TrimSpace(b.Name) == "" {
		return errors.New("notify: blackout name is required")
	}
	if b.At.IsZero() {
		return errors.New("notify: blackout instant is required")
	}
	if b.Before < 0 || b.After < 0 {
		return errors.New("notify: blackout window cannot be negative")
	}
	return nil
}

// Reason explains a suppression. An empty Reason means delivery is allowed.
type Reason struct {
	Blocked bool
	Why     string
}

// Allow reports whether a signal from strategy on symbol detected at
// detectedAt may be delivered at now, and records the decision when allowed.
//
// The dedupe and symbol-cooldown keys include the strategy, so two different
// strategies may each signal the same instrument, but one strategy cannot
// notify twice for the same bar. The correlated-group cap is per group across
// ALL strategies: two strategies both going long the dollar at once is still
// one bet sized twice.
func (g *Gate) Allow(strategy, symbol string, detectedAt, now time.Time) Reason {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.lastByGroup == nil {
		g.lastByGroup = map[string]time.Time{}
		g.lastBySym = map[string]time.Time{}
		g.lastDetect = map[string]time.Time{}
	}
	key := strategy + "\x00" + symbol

	if last, seen := g.lastDetect[key]; seen && !detectedAt.After(last) {
		return Reason{Blocked: true, Why: "already delivered for this bar"}
	}

	for _, blackout := range g.Blackouts {
		if blackout.covers(now) {
			return Reason{Blocked: true, Why: "blackout: " + blackout.Name +
				" at " + blackout.At.UTC().Format("15:04") + " UTC"}
		}
	}

	if g.SymbolCooldown > 0 {
		if last, seen := g.lastBySym[key]; seen && now.Sub(last) < g.SymbolCooldown {
			return Reason{Blocked: true, Why: "symbol cooldown active"}
		}
	}

	group := g.groupOf(symbol)
	if group != "" && g.GroupCooldown > 0 {
		if last, seen := g.lastByGroup[group]; seen && now.Sub(last) < g.GroupCooldown {
			return Reason{Blocked: true, Why: "correlated group " + group + " already has a live signal"}
		}
	}

	g.lastDetect[key] = detectedAt
	g.lastBySym[key] = now
	if group != "" {
		g.lastByGroup[group] = now
	}
	return Reason{}
}

func (g *Gate) groupOf(symbol string) string {
	if g.Groups == nil {
		return ""
	}
	return g.Groups[strings.ToUpper(symbol)]
}

// NextBlackout returns the soonest blackout at or after now, for startup
// reporting. ok is false when none remain.
func (g *Gate) NextBlackout(now time.Time) (Blackout, bool) {
	pending := make([]Blackout, 0, len(g.Blackouts))
	for _, blackout := range g.Blackouts {
		if !blackout.At.Add(blackout.After).Before(now) {
			pending = append(pending, blackout)
		}
	}
	if len(pending) == 0 {
		return Blackout{}, false
	}
	sort.Slice(pending, func(i, j int) bool { return pending[i].At.Before(pending[j].At) })
	return pending[0], true
}

// DefaultCorrelationGroups groups instruments that express the same macro
// bet. The dollar group is deliberately coarse: during a dollar move these
// pairs are one position, not six.
func DefaultCorrelationGroups() map[string]string {
	groups := map[string]string{}
	for _, symbol := range []string{"EURUSD", "GBPUSD", "AUDUSD", "NZDUSD"} {
		groups[symbol] = "USD_SHORT_SIDE"
	}
	for _, symbol := range []string{"USDCAD", "USDCHF", "USDJPY"} {
		groups[symbol] = "USD_LONG_SIDE"
	}
	for _, symbol := range []string{"XAUUSD", "XAGUSD"} {
		groups[symbol] = "METALS"
	}
	return groups
}
