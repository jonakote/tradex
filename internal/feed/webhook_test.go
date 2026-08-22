package feed

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestWebhookAlertValidation(t *testing.T) {
	valid := WebhookAlert{
		Symbol: "EUR/USD", Interval: "1h", Action: "candle_close",
		Open: 1.08400, High: 1.08500, Low: 1.08300, Close: 1.08450,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid rejected: %v", err)
	}

	t.Run("missing symbol", func(t *testing.T) {
		a := valid
		a.Symbol = ""
		if err := a.Validate(); err == nil {
			t.Fatal("accepted")
		}
	})
	t.Run("candle_close needs OHLC", func(t *testing.T) {
		a := valid
		a.Open = 0
		if err := a.Validate(); err == nil {
			t.Fatal("accepted")
		}
	})
	t.Run("signal needs SL/TP", func(t *testing.T) {
		a := WebhookAlert{Symbol: "XAU/USD", Interval: "15min", Action: "signal_buy"}
		if err := a.Validate(); err == nil {
			t.Fatal("accepted signal without SL/TP")
		}
	})
	t.Run("valid signal", func(t *testing.T) {
		a := WebhookAlert{
			Symbol: "XAU/USD", Interval: "15min", Action: "signal_buy",
			StopLoss: 1940.0, TakeProfit: 1970.0, Entry: 1950.0,
		}
		if err := a.Validate(); err != nil {
			t.Fatalf("valid signal rejected: %v", err)
		}
	})
	t.Run("unknown action", func(t *testing.T) {
		a := WebhookAlert{Symbol: "X", Interval: "1h", Action: "invalid"}
		if err := a.Validate(); err == nil {
			t.Fatal("accepted unknown action")
		}
	})
}

func TestWebhookAlertToCandle(t *testing.T) {
	alert := WebhookAlert{
		Symbol: "GBP/USD", Interval: "1h", Action: "candle_close",
		Open: 1.27000, High: 1.27200, Low: 1.26800, Close: 1.27100,
		Volume: 500, Time: "2026-08-20T14:00:00Z",
	}
	candle, err := alert.ToCandle()
	if err != nil {
		t.Fatal(err)
	}
	if candle.Symbol != "GBP/USD" || candle.Close != 1.27100 {
		t.Fatalf("unexpected candle: %+v", candle)
	}
	if candle.Time.Year() != 2026 || candle.Time.Hour() != 14 {
		t.Fatalf("unexpected time: %v", candle.Time)
	}
}

func TestWebhookReceiverAcceptsValidAlert(t *testing.T) {
	receiver, alerts, err := NewWebhookReceiver(WebhookConfig{
		ListenAddr: ":0",
		Path:       "/webhook",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer receiver.Close()

	// Use the handler directly via httptest
	alert := WebhookAlert{
		Symbol: "EUR/USD", Interval: "1h", Action: "candle_close",
		Open: 1.08400, High: 1.08500, Low: 1.08300, Close: 1.08450,
		Volume: 1000, EventID: "evt-1",
	}
	body, _ := json.Marshal(alert)

	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	receiver.handleAlert(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	select {
	case received := <-alerts:
		if received.Symbol != "EUR/USD" || received.Close != 1.08450 {
			t.Fatalf("unexpected alert: %+v", received)
		}
	case <-time.After(time.Second):
		t.Fatal("no alert received")
	}
}

func TestWebhookReceiverRejectsGET(t *testing.T) {
	receiver, _, _ := NewWebhookReceiver(WebhookConfig{ListenAddr: ":0"})
	defer receiver.Close()

	req := httptest.NewRequest(http.MethodGet, "/webhook", nil)
	w := httptest.NewRecorder()
	receiver.handleAlert(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", w.Code)
	}
}

func TestWebhookReceiverRejectsInvalidJSON(t *testing.T) {
	receiver, _, _ := NewWebhookReceiver(WebhookConfig{ListenAddr: ":0"})
	defer receiver.Close()

	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader([]byte("not json")))
	w := httptest.NewRecorder()
	receiver.handleAlert(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestWebhookReceiverRejectsInvalidAlert(t *testing.T) {
	receiver, _, _ := NewWebhookReceiver(WebhookConfig{ListenAddr: ":0"})
	defer receiver.Close()

	alert := WebhookAlert{Symbol: "", Interval: "", Action: ""}
	body, _ := json.Marshal(alert)
	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(body))
	w := httptest.NewRecorder()
	receiver.handleAlert(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestWebhookReceiverSecretKey(t *testing.T) {
	receiver, _, _ := NewWebhookReceiver(WebhookConfig{
		ListenAddr: ":0",
		SecretKey:  "my-secret",
	})
	defer receiver.Close()

	alert := WebhookAlert{
		Symbol: "EUR/USD", Interval: "1h", Action: "candle_close",
		Open: 1.08400, High: 1.08500, Low: 1.08300, Close: 1.08450,
	}
	body, _ := json.Marshal(alert)

	// Without signature
	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(body))
	w := httptest.NewRecorder()
	receiver.handleAlert(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without signature, got %d", w.Code)
	}

	// With wrong signature
	req = httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(body))
	req.Header.Set("X-Tradex-Signature", "wrong")
	w = httptest.NewRecorder()
	receiver.handleAlert(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 with wrong signature, got %d", w.Code)
	}

	// With correct signature
	req = httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(body))
	req.Header.Set("X-Tradex-Signature", "my-secret")
	w = httptest.NewRecorder()
	receiver.handleAlert(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 with correct signature, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWebhookReceiverDeduplication(t *testing.T) {
	receiver, alerts, _ := NewWebhookReceiver(WebhookConfig{ListenAddr: ":0"})
	defer receiver.Close()

	alert := WebhookAlert{
		Symbol: "EUR/USD", Interval: "1h", Action: "candle_close",
		Open: 1.08400, High: 1.08500, Low: 1.08300, Close: 1.08450,
		EventID: "dedup-1",
	}
	body, _ := json.Marshal(alert)

	// First send
	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(body))
	w := httptest.NewRecorder()
	receiver.handleAlert(w, req)
	if w.Code != http.StatusOK {
		t.Fatal("first send failed")
	}

	// Drain the alert
	<-alerts

	// Second send (duplicate)
	req = httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(body))
	w = httptest.NewRecorder()
	receiver.handleAlert(w, req)
	if w.Code != http.StatusOK {
		t.Fatal("duplicate send failed")
	}

	// Should not produce another alert
	select {
	case a := <-alerts:
		t.Fatalf("received duplicate alert: %+v", a)
	case <-time.After(100 * time.Millisecond):
		// Expected
	}
}
