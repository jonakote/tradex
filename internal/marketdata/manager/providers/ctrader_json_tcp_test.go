package providers

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"testing"
	"time"

	"tradex/internal/broker/ctrader"
)

func testJSONTCPClient(t *testing.T, script func(net.Conn)) *CTraderJSONTCPDataClient {
	t.Helper()
	client, err := NewCTraderJSONTCPDataClient(CTraderJSONTCPConfig{
		Endpoint:     "pipe",
		ClientID:     "client-id",
		ClientSecret: "client-secret",
		AccountID:    "123",
		AccessToken:  func(context.Context) (string, error) { return "access-token", nil },
		DialContext: func(context.Context, string, string) (net.Conn, error) {
			local, remote := net.Pipe()
			go script(remote)
			return local, nil
		},
		RequestTimeout:  time.Second,
		HeartbeatPeriod: 10 * time.Second,
	}, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func testServerAuth(t *testing.T, conn net.Conn) error {
	t.Helper()
	first, err := readTestEnvelope(conn)
	if err != nil {
		return err
	}
	if first.PayloadType != ctraderApplicationAuthReq {
		return errors.New("expected application authorization")
	}
	if err := writeTestEnvelope(conn, jsonTCPEnvelope{ClientMsgID: first.ClientMsgID, PayloadType: ctraderApplicationAuthRes, Payload: json.RawMessage(`{}`)}); err != nil {
		return err
	}
	second, err := readTestEnvelope(conn)
	if err != nil {
		return err
	}
	if second.PayloadType != ctraderAccountsByTokenReq {
		return errors.New("expected account list request")
	}
	if err := writeTestEnvelope(conn, jsonTCPEnvelope{
		ClientMsgID: second.ClientMsgID,
		PayloadType: ctraderAccountsByTokenRes,
		Payload:     json.RawMessage(`{"ctidTraderAccount":[{"ctidTraderAccountId":123}]}`),
	}); err != nil {
		return err
	}
	third, err := readTestEnvelope(conn)
	if err != nil {
		return err
	}
	if third.PayloadType != ctraderAccountAuthReq {
		return errors.New("expected account authorization")
	}
	return writeTestEnvelope(conn, jsonTCPEnvelope{ClientMsgID: third.ClientMsgID, PayloadType: ctraderAccountAuthRes, Payload: json.RawMessage(`{}`)})
}

func TestCTraderJSONTCPDataClientAuthenticatesAndGetsTrendbars(t *testing.T) {
	client := testJSONTCPClient(t, func(conn net.Conn) {
		if err := testServerAuth(t, conn); err != nil {
			return
		}
		subscription, err := readTestEnvelope(conn)
		if err != nil || subscription.PayloadType != ctraderSubscribeSpotsReq {
			return
		}
		if err := writeTestEnvelope(conn, jsonTCPEnvelope{ClientMsgID: subscription.ClientMsgID, PayloadType: ctraderSubscribeSpotsRes, Payload: json.RawMessage(`{"ctidTraderAccountId":123}`)}); err != nil {
			return
		}
		request, err := readTestEnvelope(conn)
		if err != nil || request.PayloadType != ctraderGetTrendbarsReq {
			return
		}
		_ = writeTestEnvelope(conn, jsonTCPEnvelope{
			ClientMsgID: request.ClientMsgID,
			PayloadType: ctraderGetTrendbarsRes,
			Payload:     json.RawMessage(`{"ctidTraderAccountId":123,"period":9,"symbolId":42,"trendbar":[{"period":9,"volume":12,"low":108400,"deltaOpen":10,"deltaClose":50,"deltaHigh":120,"utcTimestampInMinutes":29900000}]}`),
		})
	})

	bars, err := client.GetTrendbars(context.Background(), "123", 42, CTraderPeriodH1, time.Time{}, time.Time{}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(bars) != 1 || bars[0].Low != 108400 || bars[0].DeltaHigh != 120 || bars[0].OpenTime.IsZero() {
		t.Fatalf("unexpected cTrader trendbars: %+v", bars)
	}
}

func TestCTraderJSONTCPDataClientGetsSpotFromSubscriptionEvent(t *testing.T) {
	client := testJSONTCPClient(t, func(conn net.Conn) {
		if err := testServerAuth(t, conn); err != nil {
			return
		}
		subscription, err := readTestEnvelope(conn)
		if err != nil || subscription.PayloadType != ctraderSubscribeSpotsReq {
			return
		}
		if err := writeTestEnvelope(conn, jsonTCPEnvelope{ClientMsgID: subscription.ClientMsgID, PayloadType: ctraderSubscribeSpotsRes, Payload: json.RawMessage(`{"ctidTraderAccountId":123}`)}); err != nil {
			return
		}
		_ = writeTestEnvelope(conn, jsonTCPEnvelope{
			PayloadType: ctraderSpotEvent,
			Payload:     json.RawMessage(`{"ctidTraderAccountId":123,"symbolId":42,"bid":108400,"ask":108402,"timestamp":1789999999000}`),
		})
	})

	spot, err := client.GetSpot(context.Background(), "123", 42)
	if err != nil {
		t.Fatal(err)
	}
	if spot.SymbolID != 42 || spot.Bid != 108400 || spot.Ask != 108402 || spot.Timestamp.IsZero() {
		t.Fatalf("unexpected cTrader spot: %+v", spot)
	}
}

func TestCTraderJSONTCPDataClientReceivesTrendbarEvents(t *testing.T) {
	client := testJSONTCPClient(t, func(conn net.Conn) {
		if err := testServerAuth(t, conn); err != nil {
			return
		}
		spotSubscription, err := readTestEnvelope(conn)
		if err != nil || spotSubscription.PayloadType != ctraderSubscribeSpotsReq {
			return
		}
		if err := writeTestEnvelope(conn, jsonTCPEnvelope{ClientMsgID: spotSubscription.ClientMsgID, PayloadType: ctraderSubscribeSpotsRes, Payload: json.RawMessage(`{"ctidTraderAccountId":123}`)}); err != nil {
			return
		}
		trendbarSubscription, err := readTestEnvelope(conn)
		if err != nil || trendbarSubscription.PayloadType != ctraderSubscribeTrendbarReq {
			return
		}
		if err := writeTestEnvelope(conn, jsonTCPEnvelope{ClientMsgID: trendbarSubscription.ClientMsgID, PayloadType: ctraderSubscribeTrendbarRes, Payload: json.RawMessage(`{"ctidTraderAccountId":123}`)}); err != nil {
			return
		}
		_ = writeTestEnvelope(conn, jsonTCPEnvelope{
			PayloadType: ctraderSpotEvent,
			Payload:     json.RawMessage(`{"ctidTraderAccountId":123,"symbolId":42,"trendbar":[{"period":9,"volume":12,"low":108400,"deltaOpen":10,"deltaClose":50,"deltaHigh":120,"utcTimestampInMinutes":29900000}]}`),
		})
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	received := make(chan CTraderTrendbar, 1)
	result := make(chan error, 1)
	go func() {
		result <- client.SubscribeTrendbars(ctx, "123", 42, CTraderPeriodH1, func(bar CTraderTrendbar) error {
			received <- bar
			cancel()
			return nil
		})
	}()

	select {
	case bar := <-received:
		if bar.SymbolID != 42 || bar.Period != CTraderPeriodH1 || bar.Low != 108400 {
			t.Fatalf("unexpected cTrader trendbar event: %+v", bar)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for cTrader trendbar event")
	}
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("expected subscription cancellation, got %v", err)
	}
}

func readTestEnvelope(conn net.Conn) (jsonTCPEnvelope, error) {
	payload, err := readTCPFrame(conn, ctraderDefaultMaxFramePayload)
	if err != nil {
		return jsonTCPEnvelope{}, err
	}
	var envelope jsonTCPEnvelope
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return jsonTCPEnvelope{}, err
	}
	return envelope, nil
}

func writeTestEnvelope(conn net.Conn, envelope jsonTCPEnvelope) error {
	payload, err := json.Marshal(envelope)
	if err != nil {
		return err
	}
	frame, err := ctrader.EncodeTCPFrame(payload)
	if err != nil {
		return err
	}
	_, err = conn.Write(frame)
	return err
}
