package feed

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"tradex/internal/instrument"
)

// A live-money credential may be in use, so this source must never be able to
// reach an endpoint that changes account state. The guard is a test rather
// than a comment because a future edit would not read the comment.
func TestCapitalSourceIsReadOnly(t *testing.T) {
	source, err := os.ReadFile("capital.go")
	if err != nil {
		t.Fatalf("read capital.go: %v", err)
	}
	// Strip comments so prose naming a forbidden path cannot fail the test,
	// and so a forbidden path cannot hide inside a comment either.
	stripped := regexp.MustCompile(`(?s)/\*.*?\*/|//[^\n]*`).ReplaceAll(source, []byte(" "))

	forbidden := []string{
		"/positions", "/workingorders", "/accounts/topUp", "/accounts/preferences",
		"/session/encryptionKey", "/history/transactions", "/history/activity",
		"/confirms", "/watchlists",
	}
	for _, path := range forbidden {
		if strings.Contains(string(stripped), path) {
			t.Errorf("capital.go references non-read-only path %q; "+
				"this source must only read market data", path)
		}
	}

	// Only GET is permitted beyond the single session POST.
	methods := regexp.MustCompile(`http\.Method[A-Za-z]+`).FindAllString(string(stripped), -1)
	for _, method := range methods {
		if method != "http.MethodGet" && method != "http.MethodPost" {
			t.Errorf("unexpected HTTP method %s", method)
		}
	}
	if got := strings.Count(string(stripped), "http.MethodPost"); got != 1 {
		t.Errorf("expected exactly one POST (the session); got %d", got)
	}
}

func TestCapitalPathAllowed(t *testing.T) {
	allowed := []string{
		capitalPathMarkets,
		capitalPathMarkets + "/EURUSD",
		capitalPathPrices + "EURUSD",
		capitalPathPrices + "GOLD",
	}
	for _, path := range allowed {
		if !capitalPathAllowed(path) {
			t.Errorf("%q should be allowed", path)
		}
	}
	denied := []string{
		"/api/v1/positions", "/api/v1/workingorders", "/api/v1/accounts/topUp",
		"/api/v1/accounts", "/api/v1/session", "", "/",
	}
	for _, path := range denied {
		if capitalPathAllowed(path) {
			t.Errorf("%q must not be allowed", path)
		}
	}
}

// decimalTicks is the only price conversion in the source, so its exactness
// is the exactness of every candle. It must never route through a float.
func TestDecimalTicks(t *testing.T) {
	cases := []struct {
		text   string
		digits int
		want   int64
	}{
		{"1.11709", 5, 111709},
		{"1.11700", 5, 111700},
		{"1.117", 5, 111700},
		{"0.8335", 5, 83350}, // trailing zeros suppressed by the vendor
		{"157.792", 3, 157792},
		{"4043.27", 3, 4043270}, // venue quotes coarser than our scale: exact
		{"51.295", 3, 51295},
		{"2.0108", 5, 201080},
		{".5", 5, 50000},
		{"7", 5, 700000},
		{"-1.5", 5, -150000},
		{"1.1170900", 5, 111709}, // extra zeros beyond our scale are lossless
	}
	for _, c := range cases {
		got, err := decimalTicks(c.text, c.digits)
		if err != nil {
			t.Errorf("decimalTicks(%q, %d) unexpected error: %v", c.text, c.digits, err)
			continue
		}
		if got != c.want {
			t.Errorf("decimalTicks(%q, %d) = %d, want %d", c.text, c.digits, got, c.want)
		}
	}

	// Fail closed when the venue is finer than the verified scale: silently
	// dropping a digit would misprice every bar invisibly.
	for _, text := range []string{"1.117095", "4043.275", "1e-5", "", "abc"} {
		if _, err := decimalTicks(text, 5); err == nil && text != "" {
			if text == "1.117095" || text == "1e-5" || text == "abc" {
				t.Errorf("decimalTicks(%q, 5) should have failed", text)
			}
		}
	}
	if _, err := decimalTicks("4043.275", 2); err == nil {
		t.Error("a price finer than the verified scale must be rejected, not truncated")
	}
}

func TestCapitalResolutionAndSide(t *testing.T) {
	for interval, want := range map[string]string{
		"1m": "MINUTE", "15m": "MINUTE_15", "1h": "HOUR", "4h": "HOUR_4",
		"1d": "DAY", "1day": "DAY", "1w": "WEEK",
	} {
		if got := capitalResolution(interval); got != want {
			t.Errorf("capitalResolution(%q) = %q, want %q", interval, got, want)
		}
	}
	// 8h and 2h have no venue resolution; a config error beats a request
	// that silently returns a different timeframe.
	for _, interval := range []string{"2h", "8h", "12h", "3d", "", "nonsense"} {
		if got := capitalResolution(interval); got != "" {
			t.Errorf("capitalResolution(%q) = %q, want empty", interval, got)
		}
	}
	if _, err := capitalSide("sideways"); err == nil {
		t.Error("an unknown book side must be rejected")
	}
}

func TestCapitalConfigValidate(t *testing.T) {
	base := CapitalConfig{
		APIKey: "k", Identifier: "i", Password: "p",
		Symbols: []string{"EURUSD"}, Interval: "1h",
	}
	if err := base.Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}

	noCreds := base
	noCreds.APIKey = ""
	if err := noCreds.Validate(); err == nil {
		t.Error("missing credentials must be rejected")
	}

	// An instrument with no verified epic must fail closed rather than have
	// its symbol passed through as an epic.
	noEpic := base
	noEpic.Symbols = []string{"BTCUSDT"}
	if err := noEpic.Validate(); err == nil {
		t.Error("a symbol with no verified capital.com epic must be rejected")
	}

	unknown := base
	unknown.Symbols = []string{"NOTAPAIR"}
	if err := unknown.Validate(); err == nil {
		t.Error("an unverified instrument must be rejected")
	}

	tooMany := base
	tooMany.Bars = capitalMaxBars + 1
	if err := tooMany.Validate(); err == nil {
		t.Error("a bar count above the venue cap must be rejected")
	}
}

func TestCapitalEpicMapping(t *testing.T) {
	if epic, ok := instrument.CapitalEpic("XAU/USD"); !ok || epic != "GOLD" {
		t.Errorf("XAU/USD -> %q, %v; want GOLD, true", epic, ok)
	}
	if epic, ok := instrument.CapitalEpic("eur-usd"); !ok || epic != "EURUSD" {
		t.Errorf("eur-usd -> %q, %v; want EURUSD, true", epic, ok)
	}
	if symbol, ok := instrument.CapitalSymbol("SILVER"); !ok || symbol != "XAGUSD" {
		t.Errorf("SILVER -> %q, %v; want XAGUSD, true", symbol, ok)
	}
	// Crypto is intentionally unmapped: Capital.com BTCUSD is not the
	// Binance BTCUSDT series already in the registry.
	if _, ok := instrument.CapitalEpic("BTCUSDT"); ok {
		t.Error("BTCUSDT must not map to a capital.com epic")
	}
	// Every epic must round-trip and name a verified instrument.
	for _, symbol := range []string{"EURUSD", "USDJPY", "XAUUSD", "XAGUSD"} {
		epic, ok := instrument.CapitalEpic(symbol)
		if !ok {
			t.Fatalf("%s has no epic", symbol)
		}
		back, ok := instrument.CapitalSymbol(epic)
		if !ok || back != symbol {
			t.Errorf("%s -> %s -> %q, %v", symbol, epic, back, ok)
		}
		if _, ok := instrument.Lookup(symbol); !ok {
			t.Errorf("%s has an epic but no verified precision", symbol)
		}
	}
}

// capitalStub serves a fixed candle payload and records the paths requested.
func capitalStub(t *testing.T, body string) (*httptest.Server, *[]string) {
	t.Helper()
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path+"?"+r.URL.RawQuery)
		if r.URL.Path == capitalPathSession {
			if r.Method != http.MethodPost {
				t.Errorf("session must be POST, got %s", r.Method)
			}
			if r.Header.Get("X-CAP-API-KEY") == "" {
				t.Error("session request carried no API key header")
			}
			w.Header().Set("CST", "cst-token")
			w.Header().Set("X-SECURITY-TOKEN", "security-token")
			_, _ = w.Write([]byte(`{"currentAccountId":"1"}`))
			return
		}
		if r.Method != http.MethodGet {
			t.Errorf("market data must be GET, got %s", r.Method)
		}
		if r.Header.Get("CST") != "cst-token" || r.Header.Get("X-SECURITY-TOKEN") != "security-token" {
			t.Error("market-data request did not carry the session tokens")
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server, &paths
}

const capitalThreeBars = `{"prices":[
 {"snapshotTime":"2026-10-01T00:00:00","snapshotTimeUTC":"2026-10-01T00:00:00",
  "openPrice":{"bid":1.17200,"ask":1.17210},"highPrice":{"bid":1.17300,"ask":1.17310},
  "lowPrice":{"bid":1.17100,"ask":1.17110},"closePrice":{"bid":1.17250,"ask":1.17260},
  "lastTradedVolume":1000},
 {"snapshotTime":"2026-10-01T01:00:00","snapshotTimeUTC":"2026-10-01T01:00:00",
  "openPrice":{"bid":1.17250,"ask":1.17260},"highPrice":{"bid":1.17400,"ask":1.17410},
  "lowPrice":{"bid":1.17200,"ask":1.17210},"closePrice":{"bid":1.17350,"ask":1.17360},
  "lastTradedVolume":2000},
 {"snapshotTime":"2026-10-01T02:00:00","snapshotTimeUTC":"2026-10-01T02:00:00",
  "openPrice":{"bid":1.17350,"ask":1.17360},"highPrice":{"bid":1.17380,"ask":1.17390},
  "lowPrice":{"bid":1.17340,"ask":1.17350},"closePrice":{"bid":1.17360,"ask":1.17370},
  "lastTradedVolume":500}
]}`

func newStubCapital(t *testing.T, server *httptest.Server, now time.Time) *Capital {
	t.Helper()
	source, err := NewCapital(CapitalConfig{
		APIKey: "k", Identifier: "i", Password: "p",
		BaseURL: server.URL, Symbols: []string{"EURUSD"}, Interval: "1h",
	})
	if err != nil {
		t.Fatalf("NewCapital: %v", err)
	}
	source.now = func() time.Time { return now }
	return source
}

// The newest bar the venue returns is still forming. Yielding it would let a
// signal repaint as the bar develops.
func TestCapitalDropsInProgressBar(t *testing.T) {
	server, paths := capitalStub(t, capitalThreeBars)
	// 02:30 UTC: the 02:00 bar owns [02:00, 03:00) and has not closed.
	now := time.Date(2026, 10, 1, 2, 30, 0, 0, time.UTC)
	candles, err := newStubCapital(t, server, now).Poll(context.Background())
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(candles) != 2 {
		t.Fatalf("got %d candles, want 2 (the forming 02:00 bar must be dropped)", len(candles))
	}
	if last := candles[len(candles)-1].OpenTime; !last.Equal(time.Date(2026, 10, 1, 1, 0, 0, 0, time.UTC)) {
		t.Errorf("newest yielded bar opens at %s, want 01:00", last)
	}
	for _, path := range *paths {
		if !strings.HasPrefix(path, capitalPathSession) && !strings.Contains(path, "/prices/EURUSD") {
			t.Errorf("unexpected request path %q", path)
		}
	}
}

// snapshotTimeUTC is the bar's OPEN, and the close is derived rather than
// taken from the vendor, so the candle owns exactly [OpenTime, CloseTime).
func TestCapitalStampsBarOpenAndDerivesClose(t *testing.T) {
	server, _ := capitalStub(t, capitalThreeBars)
	now := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	candles, err := newStubCapital(t, server, now).Poll(context.Background())
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(candles) != 3 {
		t.Fatalf("got %d candles, want 3", len(candles))
	}
	first := candles[0]
	if !first.OpenTime.Equal(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("OpenTime = %s, want the vendor snapshot time", first.OpenTime)
	}
	if !first.CloseTime.Equal(first.OpenTime.Add(time.Hour)) {
		t.Errorf("CloseTime = %s, want open plus one hour", first.CloseTime)
	}
	if first.Timeframe != time.Hour {
		t.Errorf("Timeframe = %s, want 1h", first.Timeframe)
	}
	// Bid side by default, converted to exact integer ticks.
	if first.Open != 117200 || first.Close != 117250 || first.High != 117300 || first.Low != 117100 {
		t.Errorf("unexpected OHLC ticks: %+v", first)
	}
	if first.Symbol != "EURUSD" || first.Source != "capital.com" {
		t.Errorf("provenance = %s/%s", first.Source, first.Symbol)
	}
}

// A polling source re-reads overlapping history; each bar must be emitted once.
func TestCapitalPollEmitsEachBarOnce(t *testing.T) {
	server, _ := capitalStub(t, capitalThreeBars)
	source := newStubCapital(t, server, time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC))

	first, err := source.Poll(context.Background())
	if err != nil {
		t.Fatalf("first Poll: %v", err)
	}
	second, err := source.Poll(context.Background())
	if err != nil {
		t.Fatalf("second Poll: %v", err)
	}
	if len(first) != 3 || len(second) != 0 {
		t.Errorf("polls yielded %d then %d candles, want 3 then 0", len(first), len(second))
	}
}

func TestCapitalSideSelection(t *testing.T) {
	server, _ := capitalStub(t, capitalThreeBars)
	for side, wantClose := range map[string]int64{
		"bid": 117250, "ask": 117260, "mid": 117255,
	} {
		source, err := NewCapital(CapitalConfig{
			APIKey: "k", Identifier: "i", Password: "p", BaseURL: server.URL,
			Symbols: []string{"EURUSD"}, Interval: "1h", Side: side,
		})
		if err != nil {
			t.Fatalf("NewCapital(%s): %v", side, err)
		}
		source.now = func() time.Time { return time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC) }
		candles, err := source.Poll(context.Background())
		if err != nil {
			t.Fatalf("Poll(%s): %v", side, err)
		}
		if int64(candles[0].Close) != wantClose {
			t.Errorf("side %s close = %d, want %d", side, candles[0].Close, wantClose)
		}
	}
}

// History pages the venue's 1000-bar cap and must terminate even when a
// window returns nothing, as happens across a closed market.
func TestCapitalHistoryPagesAndTerminates(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == capitalPathSession {
			w.Header().Set("CST", "cst-token")
			w.Header().Set("X-SECURITY-TOKEN", "security-token")
			_, _ = w.Write([]byte(`{}`))
			return
		}
		requests++
		if from := r.URL.Query().Get("from"); from == "" {
			t.Error("History must send an explicit from")
		}
		// Every window is empty: a market closed for the whole period.
		_, _ = w.Write([]byte(`{"prices":[]}`))
	}))
	defer server.Close()

	source := newStubCapital(t, server, time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC))
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	candles, err := source.History(context.Background(), "EURUSD", from, to)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(candles) != 0 {
		t.Errorf("got %d candles from empty windows", len(candles))
	}
	// 9 months of hourly bars at 1000 per window is a bounded number of
	// requests; an unterminating loop would blow past it.
	if requests == 0 || requests > 12 {
		t.Errorf("made %d requests, want a small bounded number", requests)
	}
}

// The venue rejects a from/to span of 1000 intervals or more, so every
// paged window must stay strictly below that boundary. Paging at exactly
// capitalMaxBars made every history request fail with
// error.invalid.max.daterange.
func TestCapitalHistoryWindowStaysInsideDateRangeCap(t *testing.T) {
	var widest time.Duration
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == capitalPathSession {
			w.Header().Set("CST", "cst-token")
			w.Header().Set("X-SECURITY-TOKEN", "security-token")
			_, _ = w.Write([]byte(`{}`))
			return
		}
		from, errFrom := time.Parse("2006-01-02T15:04:05", r.URL.Query().Get("from"))
		to, errTo := time.Parse("2006-01-02T15:04:05", r.URL.Query().Get("to"))
		if errFrom != nil || errTo != nil {
			t.Fatalf("History sent unparseable window %q..%q",
				r.URL.Query().Get("from"), r.URL.Query().Get("to"))
		}
		if span := to.Sub(from); span > widest {
			widest = span
		}
		_, _ = w.Write([]byte(`{"prices":[]}`))
	}))
	defer server.Close()

	source := newStubCapital(t, server, time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC))
	from := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := source.History(context.Background(), "EURUSD", from, to); err != nil {
		t.Fatalf("History: %v", err)
	}
	limit := time.Duration(capitalMaxBars) * time.Hour
	if widest >= limit {
		t.Errorf("widest requested window %s reaches the venue cap of %s", widest, limit)
	}
	if widest == 0 {
		t.Error("no window was requested")
	}
}

// A 404 on one history window means the venue holds no bars for it. The walk
// must step over it rather than discard everything already fetched.
func TestCapitalHistoryTreatsNotFoundWindowAsEmpty(t *testing.T) {
	windows := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == capitalPathSession {
			w.Header().Set("CST", "cst-token")
			w.Header().Set("X-SECURITY-TOKEN", "security-token")
			_, _ = w.Write([]byte(`{}`))
			return
		}
		windows++
		if windows == 1 {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"errorCode":"error.prices.not-found"}`))
			return
		}
		_, _ = w.Write([]byte(capitalThreeBars))
	}))
	defer server.Close()

	source := newStubCapital(t, server, time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC))
	from := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)
	candles, err := source.History(context.Background(), "EURUSD", from, to)
	if err != nil {
		t.Fatalf("a 404 window must not fail the walk: %v", err)
	}
	if windows < 2 {
		t.Fatalf("only %d windows requested; the walk stopped at the 404", windows)
	}
	if len(candles) == 0 {
		t.Error("bars from the windows after the 404 were lost")
	}

	// A live poll must stay loud: 404 there means a wrong epic.
	source.cursor = newCursor()
	notFound := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == capitalPathSession {
			w.Header().Set("CST", "c")
			w.Header().Set("X-SECURITY-TOKEN", "s")
			_, _ = w.Write([]byte(`{}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"errorCode":"error.prices.not-found"}`))
	}))
	defer notFound.Close()
	polling := newStubCapital(t, notFound, time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC))
	if _, err := polling.Poll(context.Background()); err == nil {
		t.Error("a 404 on a live poll must be reported, not swallowed")
	}
}

// An expired session must self-heal exactly once rather than loop.
func TestCapitalReauthenticatesOnceOn401(t *testing.T) {
	sessions, prices := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == capitalPathSession {
			sessions++
			w.Header().Set("CST", "cst-token")
			w.Header().Set("X-SECURITY-TOKEN", "security-token")
			_, _ = w.Write([]byte(`{}`))
			return
		}
		prices++
		if prices == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"errorCode":"error.invalid.session.token"}`))
			return
		}
		_, _ = w.Write([]byte(capitalThreeBars))
	}))
	defer server.Close()

	source := newStubCapital(t, server, time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC))
	candles, err := source.Poll(context.Background())
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(candles) != 3 {
		t.Errorf("got %d candles after re-auth, want 3", len(candles))
	}
	if sessions != 2 || prices != 2 {
		t.Errorf("sessions=%d prices=%d, want 2 and 2", sessions, prices)
	}
}

// A venue error must surface its code without echoing any credential.
func TestCapitalErrorsNeverLeakCredentials(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"errorCode":"error.null.accountId"}`))
	}))
	defer server.Close()

	source, err := NewCapital(CapitalConfig{
		APIKey: "SECRETKEY", Identifier: "user@example.com", Password: "SECRETPASS",
		BaseURL: server.URL, Symbols: []string{"EURUSD"}, Interval: "1h",
	})
	if err != nil {
		t.Fatalf("NewCapital: %v", err)
	}
	_, err = source.Poll(context.Background())
	if err == nil {
		t.Fatal("expected an error")
	}
	message := err.Error()
	if !strings.Contains(message, "error.null.accountId") {
		t.Errorf("error should surface the venue code, got %q", message)
	}
	for _, secret := range []string{"SECRETKEY", "SECRETPASS", "user@example.com"} {
		if strings.Contains(message, secret) {
			t.Errorf("error message leaked a credential: %q", message)
		}
	}
}

// The source must satisfy the one Source contract.
func TestCapitalImplementsSource(t *testing.T) {
	var _ Source = (*Capital)(nil)
}

// Guard the payload shape this adapter depends on: both book sides present on
// every OHLC field is what makes the historical spread measurable.
func TestCapitalPayloadCarriesBothBookSides(t *testing.T) {
	var response struct {
		Prices []capitalPrice `json:"prices"`
	}
	decoder := json.NewDecoder(strings.NewReader(capitalThreeBars))
	decoder.UseNumber()
	if err := decoder.Decode(&response); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, price := range response.Prices {
		for name, quote := range map[string]capitalQuote{
			"open": price.Open, "high": price.High, "low": price.Low, "close": price.Close,
		} {
			if quote.Bid == nil || quote.Ask == nil {
				t.Errorf("%s bar %s is missing a book side", name, price.SnapshotTimeUTC)
			}
		}
	}
}
