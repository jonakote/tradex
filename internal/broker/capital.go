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
	// confirm's own dealId field.
	for i := 0; i < 3; i++ {
		positions, err := c.OpenPositions()
		if err == nil {
			for _, p := range positions {
				if p.Epic == epic && p.Direction == direction && p.Size == size {
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
