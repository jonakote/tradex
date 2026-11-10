package feed

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"tradex/internal/domain"
	"tradex/internal/instrument"
)

// Capital.com REST market data.
//
// This source is READ ONLY by construction. It builds requests for exactly
// the three paths below and nothing else, because the credential in use may
// belong to a live-money account: a defect that reached an order endpoint
// would spend real funds. capital_test.go asserts that no order, position or
// account-mutating path appears anywhere in this file, so the restriction
// survives future edits instead of resting on a comment.
const (
	capitalPathSession = "/api/v1/session"
	capitalPathMarkets = "/api/v1/markets"
	capitalPathPrices  = "/api/v1/prices/"

	capitalDemoBaseURL = "https://demo-api-capital.backend-capital.com"
	capitalLiveBaseURL = "https://api-capital.backend-capital.com"

	// The venue caps a single candle request at 1000 bars (default 10).
	capitalMaxBars = 1000

	// Separately from the bar cap, the venue rejects a from/to span of
	// 1000 intervals or more with error.invalid.max.daterange. Measured
	// boundaries: HOUR accepts 41 days and refuses 42 (1000h = 41.7d);
	// HOUR_4 accepts 165 and refuses more; MINUTE_15 accepts 10 days and
	// refuses 11; DAY accepts 900 and refuses 1000. Paging at exactly
	// 1000 intervals therefore fails on every request, so windows are
	// sized below the boundary.
	capitalMaxWindowBars = 960

	// A session is dropped after 10 minutes idle; renew early.
	capitalSessionTTL = 8 * time.Minute
)

// CapitalConfig configures the Capital.com candle source.
//
// Credentials are read from the environment by the caller and never logged.
// Password is the API key's custom password, not the account login password.
type CapitalConfig struct {
	APIKey     string   `json:"api_key,omitempty"`
	Identifier string   `json:"identifier,omitempty"`
	Password   string   `json:"password,omitempty"`
	BaseURL    string   `json:"base_url,omitempty"`
	Symbols    []string `json:"symbols"`
	Interval   string   `json:"interval"`
	// Side selects which book side becomes the candle: "bid" (default),
	// "ask" or "mid". It is explicit because a strategy measured on bid
	// candles is not the same strategy on ask candles, and Dukascopy
	// research files are bid.
	Side string `json:"side,omitempty"`
	// Bars per poll, 1..1000. Defaults to 200: enough to rebuild state
	// after a restart without re-reading history on every tick.
	Bars    int           `json:"bars,omitempty"`
	Timeout time.Duration `json:"timeout,omitempty"`
}

func (c CapitalConfig) Validate() error {
	if c.APIKey == "" || c.Identifier == "" || c.Password == "" {
		return errors.New("feed: capital.com api key, identifier and password are required")
	}
	if len(c.Symbols) == 0 {
		return ErrNoSymbols
	}
	if capitalResolution(c.Interval) == "" {
		return fmt.Errorf("feed: capital.com has no resolution for interval %q", c.Interval)
	}
	if _, err := capitalSide(c.Side); err != nil {
		return err
	}
	if c.Bars < 0 || c.Bars > capitalMaxBars {
		return fmt.Errorf("feed: capital.com bars must be 0 (default) or 1..%d", capitalMaxBars)
	}
	for _, symbol := range c.Symbols {
		if _, err := resolve(symbol); err != nil {
			return err
		}
		if _, ok := instrument.CapitalEpic(symbol); !ok {
			return fmt.Errorf("feed: no verified capital.com epic for %s; "+
				"add it to internal/instrument after checking GET %s", symbol, capitalPathMarkets)
		}
	}
	return nil
}

func (c CapitalConfig) baseURL() string {
	if c.BaseURL != "" {
		return strings.TrimRight(c.BaseURL, "/")
	}
	return capitalDemoBaseURL
}

func (c CapitalConfig) bars() int {
	if c.Bars > 0 {
		return c.Bars
	}
	return 200
}

func (c CapitalConfig) timeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return 30 * time.Second
}

// capitalResolution maps a tradex interval to a Capital.com resolution. It
// returns "" for an interval the venue cannot serve, so a config error is
// raised instead of a request that silently returns another timeframe.
func capitalResolution(interval string) string {
	switch strings.TrimSpace(interval) {
	case "1m", "1min":
		return "MINUTE"
	case "5m", "5min":
		return "MINUTE_5"
	case "15m", "15min":
		return "MINUTE_15"
	case "30m", "30min":
		return "MINUTE_30"
	case "1h", "60m", "1hour":
		return "HOUR"
	case "4h":
		return "HOUR_4"
	case "1d", "1day":
		return "DAY"
	case "1w", "1week":
		return "WEEK"
	}
	return ""
}

type capitalBookSide int

const (
	capitalBid capitalBookSide = iota
	capitalAsk
	capitalMid
)

func capitalSide(side string) (capitalBookSide, error) {
	switch strings.ToLower(strings.TrimSpace(side)) {
	case "", "bid":
		return capitalBid, nil
	case "ask", "offer":
		return capitalAsk, nil
	case "mid":
		return capitalMid, nil
	}
	return capitalBid, fmt.Errorf("feed: capital.com side must be bid, ask or mid, got %q", side)
}

// Capital is a read-only Source over the Capital.com REST candle endpoint.
type Capital struct {
	config CapitalConfig
	side   capitalBookSide
	client *http.Client
	cursor *cursor
	now    func() time.Time

	mu       sync.Mutex
	cst      string
	security string
	authAt   time.Time
}

// NewCapital creates a Capital.com candle source. No network call is made
// until the first Poll.
func NewCapital(config CapitalConfig) (*Capital, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	side, err := capitalSide(config.Side)
	if err != nil {
		return nil, err
	}
	return &Capital{
		config: config,
		side:   side,
		client: &http.Client{Timeout: config.timeout()},
		cursor: newCursor(),
		now:    time.Now,
	}, nil
}

func (c *Capital) Name() string      { return "capital.com" }
func (c *Capital) Interval() string  { return c.config.Interval }
func (c *Capital) Symbols() []string { return c.config.Symbols }

// Poll fetches each configured symbol and returns only bars that have closed
// and were not yielded by an earlier call.
func (c *Capital) Poll(ctx context.Context) ([]domain.Candle, error) {
	var all []domain.Candle
	for _, symbol := range c.config.Symbols {
		if err := ctx.Err(); err != nil {
			return all, err
		}
		candles, err := c.poll(ctx, symbol)
		if err != nil {
			return all, err
		}
		all = append(all, candles...)
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].OpenTime.Before(all[j].OpenTime) })
	return all, nil
}

func (c *Capital) poll(ctx context.Context, symbol string) ([]domain.Candle, error) {
	query := url.Values{}
	query.Set("resolution", capitalResolution(c.config.Interval))
	query.Set("max", strconv.Itoa(c.config.bars()))
	candles, err := c.fetch(ctx, symbol, query, false)
	if err != nil {
		return nil, err
	}
	key := instrument.Normalize(symbol) + "\x00" + c.config.Interval
	return c.cursor.advance(key, candles), nil
}

// History returns completed candles for an explicit window, paging through
// the venue's 1000-bar-per-request cap. It is the research path: Poll keeps
// a live engine current, History builds a dataset.
//
// Bars are returned in ascending open-time order with no duplicates.
func (c *Capital) History(ctx context.Context, symbol string, from, to time.Time) ([]domain.Candle, error) {
	timeframe := IntervalDuration(c.config.Interval)
	if timeframe <= 0 {
		return nil, fmt.Errorf("feed: unknown interval %q", c.config.Interval)
	}
	if !to.After(from) {
		return nil, errors.New("feed: history window requires to after from")
	}

	seen := map[int64]struct{}{}
	var out []domain.Candle
	windowStart := from.UTC()
	for windowStart.Before(to) {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		// Request a window inside both the bar cap and the date-range cap.
		end := windowStart.Add(time.Duration(capitalMaxWindowBars) * timeframe)
		if end.After(to) {
			end = to
		}
		query := url.Values{}
		query.Set("resolution", capitalResolution(c.config.Interval))
		query.Set("max", strconv.Itoa(capitalMaxBars))
		query.Set("from", capitalTime(windowStart))
		query.Set("to", capitalTime(end))

		batch, err := c.fetch(ctx, symbol, query, true)
		if err != nil {
			return out, err
		}

		added := 0
		var newest time.Time
		for _, candle := range batch {
			key := candle.OpenTime.UnixMilli()
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, candle)
			added++
			if candle.OpenTime.After(newest) {
				newest = candle.OpenTime
			}
		}
		// Advance past the newest bar received. When a window yields
		// nothing — a market closed for the whole period — step over it
		// rather than spinning on the same request forever.
		if added > 0 && newest.Add(timeframe).After(windowStart) {
			windowStart = newest.Add(timeframe)
		} else {
			windowStart = end
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].OpenTime.Before(out[j].OpenTime) })
	return out, nil
}

// Snapshot returns the venue's current bid/ask for one instrument.
//
// This is the live quote, not a bar: it answers "what is the price right now",
// which a candle source structurally cannot, since the newest bar it will
// yield is the last one that CLOSED. Use it for display and sanity checks,
// never as a substitute for a completed candle in strategy logic.
func (c *Capital) Snapshot(ctx context.Context, symbol string) (domain.Quote, error) {
	spec, err := resolve(symbol)
	if err != nil {
		return domain.Quote{}, err
	}
	epic, ok := instrument.CapitalEpic(symbol)
	if !ok {
		return domain.Quote{}, fmt.Errorf("feed: no verified capital.com epic for %s", symbol)
	}

	payload, err := c.authed(ctx, capitalPathMarkets+"/"+epic, nil)
	if err != nil {
		return domain.Quote{}, fmt.Errorf("feed: capital.com market %s: %w", epic, err)
	}

	var response struct {
		Snapshot struct {
			MarketStatus  string       `json:"marketStatus"`
			UpdateTime    string       `json:"updateTime"`
			DelayTime     *json.Number `json:"delayTime"`
			Bid           *json.Number `json:"bid"`
			Offer         *json.Number `json:"offer"`
			DecimalPlaces *int         `json:"decimalPlacesFactor"`
		} `json:"snapshot"`
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	if err := decoder.Decode(&response); err != nil {
		return domain.Quote{}, fmt.Errorf("feed: capital.com decode %s: %w", epic, err)
	}
	snapshot := response.Snapshot

	// The venue declares its own precision here. A venue quoting FINER than
	// the verified scale would be silently truncated, so refuse; a coarser
	// venue quote converts exactly and is fine.
	if snapshot.DecimalPlaces != nil && *snapshot.DecimalPlaces > spec.Digits {
		return domain.Quote{}, fmt.Errorf(
			"feed: capital.com quotes %s at %d decimals but internal/instrument has %d; "+
				"verify and widen the scale before trusting any tick-denominated value",
			epic, *snapshot.DecimalPlaces, spec.Digits)
	}

	bid, err := capitalTicks(snapshot.Bid, spec)
	if err != nil {
		return domain.Quote{}, fmt.Errorf("feed: capital.com %s bid: %w", epic, err)
	}
	ask, err := capitalTicks(snapshot.Offer, spec)
	if err != nil {
		return domain.Quote{}, fmt.Errorf("feed: capital.com %s offer: %w", epic, err)
	}

	// updateTime carries no zone but is UTC, confirmed against the venue's
	// own serverTime. Parse both shapes: with and without milliseconds.
	at := c.now().UTC()
	for _, layout := range []string{"2006-01-02T15:04:05.000", "2006-01-02T15:04:05"} {
		if parsed, err := time.Parse(layout, snapshot.UpdateTime); err == nil {
			at = parsed.UTC()
			break
		}
	}

	quote := domain.Quote{
		Source: "capital.com", Symbol: spec.Symbol,
		Bid: bid, Ask: ask, At: at,
	}
	if err := quote.Validate(); err != nil {
		return domain.Quote{}, fmt.Errorf("feed: capital.com %s snapshot: %w (market status %s)",
			epic, err, snapshot.MarketStatus)
	}
	return quote, nil
}

// MarketStatus reports whether the venue is currently quoting the instrument,
// so a stale-looking price can be distinguished from a closed market.
func (c *Capital) MarketStatus(ctx context.Context, symbol string) (string, error) {
	epic, ok := instrument.CapitalEpic(symbol)
	if !ok {
		return "", fmt.Errorf("feed: no verified capital.com epic for %s", symbol)
	}
	payload, err := c.authed(ctx, capitalPathMarkets+"/"+epic, nil)
	if err != nil {
		return "", err
	}
	var response struct {
		Snapshot struct {
			MarketStatus string `json:"marketStatus"`
		} `json:"snapshot"`
	}
	if err := json.Unmarshal(payload, &response); err != nil {
		return "", err
	}
	return response.Snapshot.MarketStatus, nil
}

// fetch performs one candle request and converts the payload.
func (c *Capital) fetch(ctx context.Context, symbol string, query url.Values,
	tolerateEmpty bool) ([]domain.Candle, error) {

	spec, err := resolve(symbol)
	if err != nil {
		return nil, err
	}
	epic, ok := instrument.CapitalEpic(symbol)
	if !ok {
		return nil, fmt.Errorf("feed: no verified capital.com epic for %s", symbol)
	}

	payload, status, err := c.authedStatus(ctx, capitalPathPrices+epic, query)
	if err != nil {
		// The venue answers 404 error.prices.not-found for a window it
		// holds no bars for — a market closed for the whole period, or a
		// span before its history begins. For a history walk that is data,
		// not a failure: aborting there would discard every year already
		// fetched. A live poll keeps it fatal, because 404 then means the
		// epic is wrong.
		if tolerateEmpty && status == http.StatusNotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("feed: capital.com prices %s: %w", epic, err)
	}

	var response struct {
		Prices []capitalPrice `json:"prices"`
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	if err := decoder.Decode(&response); err != nil {
		return nil, fmt.Errorf("feed: capital.com decode %s: %w", epic, err)
	}

	timeframe := IntervalDuration(c.config.Interval)
	now := c.now().UTC()
	candles := make([]domain.Candle, 0, len(response.Prices))
	for _, price := range response.Prices {
		candle, err := price.candle(spec, c.side, timeframe)
		if err != nil {
			return nil, fmt.Errorf("feed: capital.com %s bar %s: %w", epic, price.SnapshotTimeUTC, err)
		}
		// The newest bar returned is the one still forming. A signal
		// derived from it repaints as the bar develops.
		if candle.CloseTime.After(now) {
			continue
		}
		candles = append(candles, candle)
	}
	return candles, nil
}

// capitalPrice is one bar as published. Every OHLC field carries both book
// sides, which is why the historical spread is measurable per bar.
type capitalPrice struct {
	SnapshotTime    string       `json:"snapshotTime"`
	SnapshotTimeUTC string       `json:"snapshotTimeUTC"`
	Open            capitalQuote `json:"openPrice"`
	High            capitalQuote `json:"highPrice"`
	Low             capitalQuote `json:"lowPrice"`
	Close           capitalQuote `json:"closePrice"`
	Volume          *json.Number `json:"lastTradedVolume"`
}

type capitalQuote struct {
	Bid *json.Number `json:"bid"`
	Ask *json.Number `json:"ask"`
}

// candle converts one published bar into a validated domain.Candle.
//
// snapshotTimeUTC denotes the bar's OPEN, verified by aligning 525 of 525
// hourly bars against Dukascopy over October 2026 with no shift. The close is
// derived as open plus timeframe so the owned interval is exactly
// [OpenTime, CloseTime).
func (p capitalPrice) candle(spec instrument.Spec, side capitalBookSide,
	timeframe time.Duration) (domain.Candle, error) {

	openTime, err := time.Parse("2006-01-02T15:04:05", p.SnapshotTimeUTC)
	if err != nil {
		return domain.Candle{}, fmt.Errorf("unparseable snapshotTimeUTC: %w", err)
	}
	openTime = openTime.UTC()

	prices := make([]domain.Price, 4)
	for i, quote := range []capitalQuote{p.Open, p.High, p.Low, p.Close} {
		if prices[i], err = quote.price(spec, side); err != nil {
			return domain.Candle{}, err
		}
	}

	// A bar with no trades still occupies time; TickCount must be positive
	// for domain validation, so an absent or zero volume becomes 1 rather
	// than inventing activity.
	volume := uint64(1)
	if p.Volume != nil {
		if parsed, err := strconv.ParseFloat(p.Volume.String(), 64); err == nil && parsed >= 1 {
			volume = uint64(parsed)
		}
	}

	candle := domain.Candle{
		Source: "capital.com", Symbol: spec.Symbol, Timeframe: timeframe,
		OpenTime: openTime, CloseTime: openTime.Add(timeframe),
		Open: prices[0], High: prices[1], Low: prices[2], Close: prices[3],
		TickCount: volume,
	}
	if err := candle.Validate(); err != nil {
		return domain.Candle{}, err
	}
	return candle, nil
}

// price selects the configured book side and converts it to integer ticks.
func (q capitalQuote) price(spec instrument.Spec, side capitalBookSide) (domain.Price, error) {
	switch side {
	case capitalAsk:
		return capitalTicks(q.Ask, spec)
	case capitalMid:
		bid, err := capitalTicks(q.Bid, spec)
		if err != nil {
			return 0, err
		}
		ask, err := capitalTicks(q.Ask, spec)
		if err != nil {
			return 0, err
		}
		// Integer midpoint; halves round toward the bid deliberately so
		// the value is reproducible rather than platform-dependent.
		return bid + (ask-bid)/2, nil
	default:
		return capitalTicks(q.Bid, spec)
	}
}

func capitalTicks(number *json.Number, spec instrument.Spec) (domain.Price, error) {
	if number == nil {
		return 0, errors.New("missing book side")
	}
	ticks, err := decimalTicks(number.String(), spec.Digits)
	if err != nil {
		return 0, err
	}
	return domain.Price(ticks), nil
}

// decimalTicks converts a decimal price string into integer ticks WITHOUT
// going through a float, so the conversion is exact and reproducible.
//
// It fails closed when the venue quotes more precision than the instrument's
// verified scale can hold, because silently truncating a digit would misprice
// every bar by an amount too small to notice and too large to ignore. That is
// the signal to verify the instrument and widen its scale, not to round.
func decimalTicks(text string, digits int) (int64, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0, errors.New("empty price")
	}
	negative := false
	switch text[0] {
	case '+':
		text = text[1:]
	case '-':
		negative, text = true, text[1:]
	}
	if strings.ContainsAny(text, "eE") {
		return 0, fmt.Errorf("exponent notation in price %q is not supported", text)
	}

	whole, fraction := text, ""
	if dot := strings.IndexByte(text, '.'); dot >= 0 {
		whole, fraction = text[:dot], text[dot+1:]
	}
	if whole == "" {
		whole = "0"
	}
	if len(fraction) > digits {
		if strings.Trim(fraction[digits:], "0") != "" {
			return 0, fmt.Errorf("price %q carries more precision than the verified %d digits "+
				"for this instrument; verify the venue scale in internal/instrument", text, digits)
		}
		fraction = fraction[:digits]
	}
	for len(fraction) < digits {
		fraction += "0"
	}

	ticks, err := strconv.ParseInt(whole+fraction, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("unparseable price %q", text)
	}
	if negative {
		ticks = -ticks
	}
	return ticks, nil
}

// capitalTime formats an instant the way the venue's from/to parameters
// expect: second-resolution UTC with no zone suffix.
func capitalTime(at time.Time) string {
	return at.UTC().Format("2006-01-02T15:04:05")
}

// authed issues a GET against an allowed path, establishing or renewing the
// session as needed. A 401 triggers exactly one re-authentication, so an
// expired session self-heals without looping on a bad credential.
func (c *Capital) authed(ctx context.Context, path string, query url.Values) ([]byte, error) {
	payload, _, err := c.authedStatus(ctx, path, query)
	return payload, err
}

// authedStatus also reports the HTTP status, so a caller can distinguish
// "this window holds no data" from "this request was wrong".
func (c *Capital) authedStatus(ctx context.Context, path string, query url.Values) ([]byte, int, error) {
	var lastStatus int
	for attempt := 0; attempt < 2; attempt++ {
		cst, security, err := c.session(ctx, attempt > 0)
		if err != nil {
			return nil, 0, err
		}
		payload, status, err := c.get(ctx, path, query, map[string]string{
			"CST": cst, "X-SECURITY-TOKEN": security,
		})
		lastStatus = status
		if err == nil {
			return payload, status, nil
		}
		if status != http.StatusUnauthorized || attempt == 1 {
			return nil, status, err
		}
	}
	return nil, lastStatus, errors.New("feed: capital.com authentication retry exhausted")
}

// session returns a valid token pair, creating one when absent, expired, or
// when force is set after a 401.
func (c *Capital) session(ctx context.Context, force bool) (string, string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !force && c.cst != "" && c.now().UTC().Sub(c.authAt) < capitalSessionTTL {
		return c.cst, c.security, nil
	}

	body, err := json.Marshal(map[string]string{
		"identifier": c.config.Identifier,
		"password":   c.config.Password,
	})
	if err != nil {
		return "", "", err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.config.baseURL()+capitalPathSession, bytes.NewReader(body))
	if err != nil {
		return "", "", err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CAP-API-KEY", c.config.APIKey)

	response, err := c.client.Do(request)
	if err != nil {
		return "", "", err
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return "", "", err
	}
	if response.StatusCode != http.StatusOK {
		// The venue's errorCode is safe to surface; the credentials are
		// never echoed.
		return "", "", fmt.Errorf("feed: capital.com session failed: status %d: %s",
			response.StatusCode, capitalErrorCode(payload))
	}
	cst := response.Header.Get("CST")
	security := response.Header.Get("X-SECURITY-TOKEN")
	if cst == "" || security == "" {
		return "", "", errors.New("feed: capital.com session returned no CST/X-SECURITY-TOKEN")
	}
	c.cst, c.security, c.authAt = cst, security, c.now().UTC()
	return cst, security, nil
}

func (c *Capital) get(ctx context.Context, path string, query url.Values,
	headers map[string]string) ([]byte, int, error) {

	if !capitalPathAllowed(path) {
		// Defence in depth: the compile-time path constants are the first
		// guard, this is the second.
		return nil, 0, fmt.Errorf("feed: capital.com path %q is not read-only", path)
	}
	requestURL := c.config.baseURL() + path
	if len(query) > 0 {
		requestURL += "?" + query.Encode()
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, 0, err
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response, err := c.client.Do(request)
	if err != nil {
		return nil, 0, err
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return nil, response.StatusCode, err
	}
	if response.StatusCode != http.StatusOK {
		return nil, response.StatusCode, fmt.Errorf("status %d: %s",
			response.StatusCode, capitalErrorCode(payload))
	}
	return payload, response.StatusCode, nil
}

// capitalPathAllowed permits only the read-only market-data paths.
func capitalPathAllowed(path string) bool {
	return path == capitalPathMarkets ||
		strings.HasPrefix(path, capitalPathMarkets+"/") ||
		strings.HasPrefix(path, capitalPathMarkets+"?") ||
		strings.HasPrefix(path, capitalPathPrices)
}

// capitalErrorCode extracts the venue's errorCode, falling back to a bounded
// slice of the body. It never returns request headers or credentials.
func capitalErrorCode(payload []byte) string {
	var body struct {
		ErrorCode string `json:"errorCode"`
	}
	if err := json.Unmarshal(payload, &body); err == nil && body.ErrorCode != "" {
		return body.ErrorCode
	}
	text := strings.TrimSpace(string(payload))
	if len(text) > 200 {
		text = text[:200]
	}
	return text
}
