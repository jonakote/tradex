package feed

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// WebhookConfig configures the TradingView webhook receiver.
type WebhookConfig struct {
	ListenAddr string `json:"listen_addr"`
	SecretKey  string `json:"secret_key"` // Optional: validates X-Tradex-Signature header
	Path       string `json:"path"`       // URL path to listen on (default: /webhook)
}

func (c WebhookConfig) Validate() error {
	if c.ListenAddr == "" {
		return errors.New("webhook listen address is required")
	}
	return nil
}

func (c WebhookConfig) path() string {
	if c.Path != "" {
		return c.Path
	}
	return "/webhook"
}

// WebhookAlert represents a parsed TradingView alert payload.
// TradingView sends arbitrary JSON/text in the webhook body.
// We define a structured format that Pine scripts should use.
type WebhookAlert struct {
	// Required
	Symbol   string `json:"symbol"`
	Interval string `json:"interval"`
	Action   string `json:"action"` // "candle_close", "signal_buy", "signal_sell", "info"

	// Candle data (when action is "candle_close")
	Open   float64 `json:"open,omitempty"`
	High   float64 `json:"high,omitempty"`
	Low    float64 `json:"low,omitempty"`
	Close  float64 `json:"close,omitempty"`
	Volume float64 `json:"volume,omitempty"`

	// Signal data (when action is "signal_buy" or "signal_sell")
	Entry      float64 `json:"entry,omitempty"`
	StopLoss   float64 `json:"stop_loss,omitempty"`
	TakeProfit float64 `json:"take_profit,omitempty"`
	Strategy   string  `json:"strategy,omitempty"`

	// Metadata
	Time      string `json:"time,omitempty"`       // ISO 8601 or TradingView format
	AlertName string `json:"alert_name,omitempty"` // For logging
	EventID   string `json:"event_id,omitempty"`   // Deduplication key
}

func (a WebhookAlert) Validate() error {
	if a.Symbol == "" || a.Interval == "" || a.Action == "" {
		return errors.New("webhook alert requires symbol, interval, and action")
	}
	switch a.Action {
	case "candle_close":
		if a.Open <= 0 || a.High <= 0 || a.Low <= 0 || a.Close <= 0 {
			return errors.New("candle_close action requires positive OHLC")
		}
	case "signal_buy", "signal_sell":
		if a.StopLoss <= 0 || a.TakeProfit <= 0 {
			return errors.New("signal action requires stop_loss and take_profit")
		}
	case "info":
		// No additional validation
	default:
		return fmt.Errorf("unknown webhook action: %s", a.Action)
	}
	return nil
}

// ToCandle converts a candle_close alert to a Candle.
func (a WebhookAlert) ToCandle() (Candle, error) {
	if a.Action != "candle_close" {
		return Candle{}, errors.New("not a candle_close alert")
	}
	t := time.Now().UTC()
	if a.Time != "" {
		if parsed, err := time.Parse(time.RFC3339, a.Time); err == nil {
			t = parsed.UTC()
		} else if parsed, err := time.Parse("2006-01-02T15:04:05", a.Time); err == nil {
			t = parsed.UTC()
		}
	}
	// TradingView's `time` is the bar's open, so a candle_close alert still
	// carries the open instant.
	barOpen, barClose := resolveBarTimes(t, a.Interval)
	return Candle{
		Symbol:    a.Symbol,
		Interval:  a.Interval,
		Open:      a.Open,
		High:      a.High,
		Low:       a.Low,
		Close:     a.Close,
		Volume:    a.Volume,
		Time:      barOpen,
		OpenTime:  barOpen,
		CloseTime: barClose,
	}, nil
}

// WebhookReceiver is an HTTP server that receives TradingView alerts.
type WebhookReceiver struct {
	config   WebhookConfig
	alertsCh chan WebhookAlert
	server   *http.Server
	seen     map[string]time.Time // event_id deduplication
}

// NewWebhookReceiver creates a webhook receiver that outputs alerts to the
// returned channel.
func NewWebhookReceiver(cfg WebhookConfig) (*WebhookReceiver, chan WebhookAlert, error) {
	if err := cfg.Validate(); err != nil {
		return nil, nil, err
	}
	ch := make(chan WebhookAlert, 64)
	r := &WebhookReceiver{
		config:   cfg,
		alertsCh: ch,
		seen:     make(map[string]time.Time),
	}
	mux := http.NewServeMux()
	mux.HandleFunc(cfg.path(), r.handleAlert)
	r.server = &http.Server{
		Addr:         cfg.ListenAddr,
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
	}
	return r, ch, nil
}

// ListenAndServe starts the webhook HTTP server. Blocks until context cancelled.
func (r *WebhookReceiver) ListenAndServe(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		errCh <- r.server.ListenAndServe()
	}()
	select {
	case <-ctx.Done():
		r.server.Close()
		return ctx.Err()
	case err := <-errCh:
		return err
	}
}

// Close shuts down the HTTP server.
func (r *WebhookReceiver) Close() error {
	return r.server.Close()
}

func (r *WebhookReceiver) handleAlert(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Optional signature check
	if r.config.SecretKey != "" {
		sig := req.Header.Get("X-Tradex-Signature")
		if sig == "" || !strings.EqualFold(sig, r.config.SecretKey) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
	}

	body, err := io.ReadAll(io.LimitReader(req.Body, 1<<16)) // 64KB max
	if err != nil {
		http.Error(w, "read error", http.StatusBadRequest)
		return
	}

	var alert WebhookAlert
	if err := json.Unmarshal(body, &alert); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	if err := alert.Validate(); err != nil {
		http.Error(w, "invalid alert: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Deduplication
	if alert.EventID != "" {
		if _, exists := r.seen[alert.EventID]; exists {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"status":"duplicate"}`))
			return
		}
		r.seen[alert.EventID] = time.Now()
		// Prune old entries (keep last 1000)
		if len(r.seen) > 1000 {
			cutoff := time.Now().Add(-1 * time.Hour)
			for id, t := range r.seen {
				if t.Before(cutoff) {
					delete(r.seen, id)
				}
			}
		}
	}

	// Send to channel (non-blocking)
	select {
	case r.alertsCh <- alert:
	default:
		// Channel full, drop (operator should consume faster)
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status":"received"}`))
}
