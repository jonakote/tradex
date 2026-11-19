// Package broker is the ONLY place in tradex allowed to place or close a
// real position on Capital.com. internal/feed is deliberately read-only
// (enforced by internal/feed/capital_test.go) — this package exists
// precisely so that boundary never has to move.
//
// Hard safety gate: NewClient refuses to construct a client at all unless
// the configured base URL contains "demo". There is currently no supported
// way to point this package at a live-money endpoint; that is intentional,
// not an oversight, until a live rollout is a deliberate, separate decision.
package broker

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Config carries the same credentials as feed.CapitalConfig, duplicated
// here rather than imported so this package never depends on (and can
// never be confused with) the read-only feed source.
type Config struct {
	APIKey     string
	Identifier string
	Password   string
	BaseURL    string
}

// Client is an authorized, order-capable Capital.com REST client.
type Client struct {
	cfg  Config
	http *http.Client

	mu       sync.Mutex
	cst      string
	security string
	authAt   time.Time
}

// NewClient refuses to run against anything that doesn't look like a demo
// endpoint. This is the single safety control that matters most: every
// other check in this package can have a bug, but if this one holds, a bug
// elsewhere costs demo dollars, not real ones.
func NewClient(cfg Config) (*Client, error) {
	if cfg.APIKey == "" || cfg.Identifier == "" || cfg.Password == "" || cfg.BaseURL == "" {
		return nil, fmt.Errorf("broker: api_key, identifier, password and base_url are all required")
	}
	if !strings.Contains(strings.ToLower(cfg.BaseURL), "demo") {
		return nil, fmt.Errorf("broker: refusing to start — base_url %q does not look like a demo endpoint; "+
			"this package will not place real-money orders without a deliberate code change, not a config flag", cfg.BaseURL)
	}
	return &Client{cfg: cfg, http: &http.Client{Timeout: 15 * time.Second}}, nil
}

func (c *Client) login() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cst != "" && time.Since(c.authAt) < 8*time.Minute {
		return nil
	}
	body, _ := json.Marshal(map[string]string{"identifier": c.cfg.Identifier, "password": c.cfg.Password})
	req, _ := http.NewRequest("POST", c.cfg.BaseURL+"/api/v1/session", bytes.NewReader(body))
	req.Header.Set("X-CAP-API-KEY", c.cfg.APIKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("broker: login: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("broker: login: status %d: %s", resp.StatusCode, string(b))
	}
	c.cst = resp.Header.Get("CST")
	c.security = resp.Header.Get("X-SECURITY-TOKEN")
	c.authAt = time.Now()
	if c.cst == "" || c.security == "" {
		return fmt.Errorf("broker: login: missing session headers")
	}
	return nil
}

func (c *Client) do(method, path string, body any, out any) error {
	if err := c.login(); err != nil {
		return err
	}
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.cfg.BaseURL+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("CST", c.cst)
	req.Header.Set("X-SECURITY-TOKEN", c.security)
	req.Header.Set("X-CAP-API-KEY", c.cfg.APIKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("broker: %s %s: status %d: %s", method, path, resp.StatusCode, string(data))
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

// Account is the current trading account's balance snapshot.
type Account struct {
	Balance   float64
	Available float64
	ProfitLoss float64
}

// AccountSummary returns the active account's balance info.
func (c *Client) AccountSummary() (Account, error) {
	var full struct {
		Accounts []struct {
			Preferred bool `json:"preferred"`
			Balance   struct {
				Balance    float64 `json:"balance"`
				Available  float64 `json:"available"`
				ProfitLoss float64 `json:"profitLoss"`
			} `json:"balance"`
		} `json:"accounts"`
	}
	if err := c.do("GET", "/api/v1/accounts", nil, &full); err != nil {
		return Account{}, err
	}
	for _, a := range full.Accounts {
		if a.Preferred {
			return Account{Balance: a.Balance.Balance, Available: a.Balance.Available, ProfitLoss: a.Balance.ProfitLoss}, nil
		}
	}
	if len(full.Accounts) > 0 {
		a := full.Accounts[0]
		return Account{Balance: a.Balance.Balance, Available: a.Balance.Available, ProfitLoss: a.Balance.ProfitLoss}, nil
	}
	return Account{}, fmt.Errorf("broker: no accounts returned")
}

// Position is one open position as Capital.com reports it.
type Position struct {
	DealID    string
	Epic      string
	Direction string
	Size      float64
	Level     float64 // entry price
	StopLevel float64
}

// OpenPositions lists every currently open position on the account.
func (c *Client) OpenPositions() ([]Position, error) {
	var out struct {
		Positions []struct {
			Position struct {
				DealID    string  `json:"dealId"`
				Direction string  `json:"direction"`
				Size      float64 `json:"size"`
				Level     float64 `json:"level"`
				StopLevel float64 `json:"stopLevel"`
			} `json:"position"`
			Market struct {
				Epic string `json:"epic"`
			} `json:"market"`
		} `json:"positions"`
	}
	if err := c.do("GET", "/api/v1/positions", nil, &out); err != nil {
		return nil, err
	}
	var positions []Position
	for _, p := range out.Positions {
		positions = append(positions, Position{
			DealID: p.Position.DealID, Epic: p.Market.Epic, Direction: p.Position.Direction,
			Size: p.Position.Size, Level: p.Position.Level, StopLevel: p.Position.StopLevel,
		})
	}
	return positions, nil
}

// ClosedFillLevel looks up the real fill price Capital.com recorded when
// a position with this epic closed via its own broker-side stop or
// take-profit (source "SL"/"TP" in the activity feed), rather than
// through our own ClosePosition call. Used only by execute.go's reverse
// reconciliation path (a locally-tracked position the broker no longer
// has open) so the recorded pnl reflects the REAL fill, not an
// approximation from the next available candle close -- found
// 2026-11-17 on US30's first live stop-out: the candle-close
// approximation happened to land on the opposite side of entry from the
// real fill, turning a real ~$19.66 loss into a recorded ~$0.82 gain.
// Returns ok=false if no matching activity is found in the lookback
// window, so the caller can fall back to the old approximation.
//
// IMPORTANT: Capital.com's /history/activity endpoint rejects any
// from/to span over roughly a day with errorCode "error.invalid.
// daterange" (silently -- c.do returns no transport error, the body
// just carries zero activities). Querying from the position's actual
// OpenedAt therefore ALWAYS failed for anything held more than ~24h,
// silently falling through to the same wrong candle approximation this
// function exists to avoid. Found 2026-11-18 on JPM (held ~27h): real
// stop fill was 323.29 (a real ~$11.30 loss), recorded as a +$1.32
// gain. Fixed by always querying a fixed, safe 20h lookback ending now
// instead of since->now -- reconciliation runs on every scan, so the
// close we're looking for happened within the last scan interval
// anyway, never days ago.
func (c *Client) ClosedFillLevel(epic string, since time.Time) (level float64, ok bool, err error) {
	lookback := time.Now().UTC().Add(-20 * time.Hour)
	if since.After(lookback) {
		lookback = since.UTC()
	}
	from := lookback.Format("2006-01-02T15:04:05")
	to := time.Now().UTC().Format("2006-01-02T15:04:05")
	var out struct {
		Activities []struct {
			Date    string `json:"date"`
			Epic    string `json:"epic"`
			Source  string `json:"source"`
			Type    string `json:"type"`
			Status  string `json:"status"`
			Details struct {
				Level float64 `json:"level"`
			} `json:"details"`
		} `json:"activities"`
	}
	path := fmt.Sprintf("/api/v1/history/activity?from=%s&to=%s&detailed=true", from, to)
	if err := c.do("GET", path, nil, &out); err != nil {
		return 0, false, err
	}
	// Walk newest-first; the first WORKING_ORDER EXECUTED with source
	// SL/TP for this epic is the real stop/take-profit fill.
	for i := len(out.Activities) - 1; i >= 0; i-- {
		a := out.Activities[i]
		if a.Epic == epic && a.Type == "WORKING_ORDER" && a.Status == "EXECUTED" &&
			(a.Source == "SL" || a.Source == "TP") && a.Details.Level > 0 {
			return a.Details.Level, true, nil
		}
	}
	return 0, false, nil
}

// PlaceOrder opens a market position with a mandatory protective stop.
// direction is "BUY" or "SELL". stopLevel is an absolute price, required —
// this package will not place a position with no broker-side stop.
//
// IMPORTANT: Capital.com's confirms endpoint has been observed to return a
// dealId that does NOT match the actual open position's dealId (a
// workingOrderId vs. position dealId mismatch) — closing by the confirm's
// ID silently fails to find the position. So after confirming ACCEPTED,
// this looks the real position up by epic/direction/size/level match and
// returns THAT dealId, never the confirm's raw field.
func (c *Client) PlaceOrder(epic, direction string, size, stopLevel float64) (dealID string, fillPrice float64, err error) {
	if stopLevel <= 0 {
		return "", 0, fmt.Errorf("broker: refusing to place %s %s: no protective stop given", direction, epic)
	}
	body := map[string]any{
		"epic":           epic,
		"direction":      direction,
		"size":           size,
		"guaranteedStop": false,
		"stopLevel":      stopLevel,
	}
	var placeResp struct {
		DealReference string `json:"dealReference"`
	}
	if err := c.do("POST", "/api/v1/positions", body, &placeResp); err != nil {
		return "", 0, err
	}
	var confirm struct {
		DealStatus string  `json:"dealStatus"`
		Reason     string  `json:"reason"`
		Level      float64 `json:"level"`
	}
	for i := 0; i < 3; i++ {
		time.Sleep(time.Duration(i) * 500 * time.Millisecond)
		if err := c.do("GET", "/api/v1/confirms/"+placeResp.DealReference, nil, &confirm); err == nil && confirm.DealStatus != "" {
			break
		}
	}
	if confirm.DealStatus != "ACCEPTED" {
		return "", 0, fmt.Errorf("broker: order rejected: %s (%s)", confirm.DealStatus, confirm.Reason)
	}
	// Resolve the real position dealId by matching, not by trusting the
	// confirm's own dealId field. Size compared with a tolerance, not
	// exact equality -- a fractional size (shares especially: 1.2, 0.1,
	// etc.) can round-trip through the API's JSON encoding with a
	// different float64 bit pattern than what we sent, so `==` can
	// miss a real, correctly-opened position. Found 2026-11-17 on the
	// very first share order (AAPL, size 1.2): the confirm showed the
	// position really opened, but the exact-equality lookup never
	// found it, and the order was incorrectly reported as failed.
	const sizeTolerance = 1e-6
	for i := 0; i < 3; i++ {
		positions, err := c.OpenPositions()
		if err == nil {
			for _, p := range positions {
				if p.Epic == epic && p.Direction == direction && math.Abs(p.Size-size) < sizeTolerance {
					return p.DealID, p.Level, nil
				}
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return "", 0, fmt.Errorf("broker: order ACCEPTED (ref %s) but the resulting position could not be found in OpenPositions — refusing to report success without a real dealId", placeResp.DealReference)
}

// ClosePosition closes an open position fully by its deal ID.
func (c *Client) ClosePosition(dealID string) error {
	req, err := http.NewRequest("DELETE", c.cfg.BaseURL+"/api/v1/positions/"+dealID, nil)
	if err != nil {
		return err
	}
	if loginErr := c.login(); loginErr != nil {
		return loginErr
	}
	req.Header.Set("CST", c.cst)
	req.Header.Set("X-SECURITY-TOKEN", c.security)
	req.Header.Set("X-CAP-API-KEY", c.cfg.APIKey)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		data, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("broker: close %s: status %d: %s", dealID, resp.StatusCode, string(data))
	}
	return nil
}
