package paper

import (
	"context"
	"sync"
	"testing"
	"time"

	"tradex/internal/clock"
	"tradex/internal/domain"
	"tradex/internal/economics"
)

func paperConfig() Config {
	instrument := domain.Instrument{
		Symbol: "EURUSD", BaseCurrency: "EUR", QuoteCurrency: "USD",
		PriceScale: 100000, TickSize: 1, QuantityStep: 1000,
		MinQuantity: 1000, MaxQuantity: 1000000,
	}
	return Config{
		AccountID: "paper-1", MaxQuoteAge: 5 * time.Second,
		Instruments: map[string]InstrumentConfig{
			"EURUSD": {
				Instrument: instrument, SlippageTicks: 1, HalfSpreadTicksForCandle: 1,
				CommissionPerQuantityStepMinor: 2,
				PriceValue:                     economics.PriceValue{AccountCurrency: "USD", Numerator: 1, Denominator: 1},
			},
		},
	}
}

func approvedMarket(at time.Time, id string) domain.ApprovedOrder {
	return domain.ApprovedOrder{
		ApprovalID: "risk-" + id, ApprovedAt: at,
		Intent: domain.OrderIntent{
			ID: id, StrategyID: "icc", StrategyVersion: "v1", AccountID: "paper-1",
			Symbol: "EURUSD", Side: domain.SideBuy, Type: domain.OrderTypeMarket,
			Quantity: 1000, StopLoss: 108390, TakeProfit: 108430,
			SignalTime: at.Add(-time.Second), ExpiresAt: at.Add(time.Minute),
		},
	}
}

func paperQuote(at time.Time, sequence uint64, bid, ask domain.Price) domain.Quote {
	return domain.Quote{Source: "paper-feed", Symbol: "EURUSD", Bid: bid, Ask: ask, At: at, Sequence: sequence}
}

func TestMarketEntryAndStopUseBidAskSlippageAndCosts(t *testing.T) {
	t0 := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	broker, err := New(paperConfig(), clock.Fixed{Time: t0.Add(time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := broker.OnQuote(paperQuote(t0, 1, 108400, 108402)); err != nil {
		t.Fatal(err)
	}
	order, err := broker.Submit(context.Background(), approvedMarket(t0.Add(time.Second), "intent-1"))
	if err != nil {
		t.Fatal(err)
	}
	if order.Status != domain.OrderFilled {
		t.Fatalf("got %s want filled", order.Status)
	}
	entry := broker.Fills()[0]
	if entry.Price != 108403 || entry.CommissionMinor != 2 || entry.Role != domain.FillEntry {
		t.Fatalf("unexpected entry: %+v", entry)
	}

	exits, err := broker.OnQuote(paperQuote(t0.Add(2*time.Second), 2, 108389, 108391))
	if err != nil {
		t.Fatal(err)
	}
	if len(exits) != 1 || exits[0].Role != domain.FillStopLoss || exits[0].Price != 108388 {
		t.Fatalf("unexpected stop fill: %+v", exits)
	}
	// Gross: (108388 - 108403) * 1 step * 1 minor = -15; net includes both commissions.
	if exits[0].RealizedGrossMinor != -15 || exits[0].RealizedNetMinor != -17 || exits[0].ClosedTradeNetMinor != -19 {
		t.Fatalf("unexpected P&L: %+v", exits[0])
	}
}

func TestSubmissionIsIdempotentUnderConcurrency(t *testing.T) {
	t0 := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	broker, _ := New(paperConfig(), clock.Fixed{Time: t0})
	approved := approvedMarket(t0, "same-intent")
	var wg sync.WaitGroup
	ids := make(chan string, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			order, err := broker.Submit(context.Background(), approved)
			if err != nil {
				t.Errorf("submit: %v", err)
				return
			}
			ids <- order.BrokerOrderID
		}()
	}
	wg.Wait()
	close(ids)
	for id := range ids {
		if id != "paper-order-000001" {
			t.Fatalf("non-idempotent order ID %s", id)
		}
	}
}

func TestLimitWaitsForExecutableSide(t *testing.T) {
	t0 := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	broker, _ := New(paperConfig(), clock.Fixed{Time: t0})
	approved := approvedMarket(t0, "limit-1")
	approved.Intent.Type = domain.OrderTypeLimit
	approved.Intent.EntryPrice = 108395
	order, err := broker.Submit(context.Background(), approved)
	if err != nil {
		t.Fatal(err)
	}
	if order.Status != domain.OrderAcknowledged {
		t.Fatalf("got %s want acknowledged", order.Status)
	}
	if fills, err := broker.OnQuote(paperQuote(t0.Add(time.Second), 1, 108400, 108402)); err != nil || len(fills) != 0 {
		t.Fatalf("early fill=%+v err=%v", fills, err)
	}
	fills, err := broker.OnQuote(paperQuote(t0.Add(2*time.Second), 2, 108393, 108395))
	if err != nil || len(fills) != 1 || fills[0].Price != 108395 {
		t.Fatalf("limit fill=%+v err=%v", fills, err)
	}
}

func TestAmbiguousCandleUsesAdverseStopFirst(t *testing.T) {
	t0 := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	broker, _ := New(paperConfig(), clock.Fixed{Time: t0.Add(time.Second)})
	_, _ = broker.OnQuote(paperQuote(t0, 1, 108400, 108402))
	if _, err := broker.Submit(context.Background(), approvedMarket(t0.Add(time.Second), "ambiguous")); err != nil {
		t.Fatal(err)
	}
	candle := domain.Candle{
		Source: "paper-feed", Symbol: "EURUSD", Timeframe: time.Minute,
		OpenTime: t0.Add(time.Minute), CloseTime: t0.Add(2 * time.Minute),
		Open: 108405, High: 108450, Low: 108370, Close: 108420, TickCount: 10,
	}
	fills, err := broker.OnCandle(candle)
	if err != nil {
		t.Fatal(err)
	}
	if len(fills) != 1 || fills[0].Role != domain.FillStopLoss {
		t.Fatalf("ambiguous candle did not choose stop: %+v", fills)
	}
}

func TestSubmissionLatencyWaitsForFirstEligibleQuote(t *testing.T) {
	t0 := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	config := paperConfig()
	instrument := config.Instruments["EURUSD"]
	instrument.SubmissionLatency = 2 * time.Second
	config.Instruments["EURUSD"] = instrument
	manual := clock.NewManual(t0)
	broker, err := New(config, manual)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := broker.OnQuote(paperQuote(t0, 1, 108400, 108402)); err != nil {
		t.Fatal(err)
	}
	if err := manual.Set(t0.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	order, err := broker.Submit(context.Background(), approvedMarket(t0.Add(time.Second), "latency"))
	if err != nil || order.Status != domain.OrderAcknowledged {
		t.Fatalf("submit=%+v err=%v", order, err)
	}
	if fills, err := broker.OnQuote(paperQuote(t0.Add(2*time.Second), 2, 108401, 108403)); err != nil || len(fills) != 0 {
		t.Fatalf("order filled before latency elapsed: %+v err=%v", fills, err)
	}
	fills, err := broker.OnQuote(paperQuote(t0.Add(3*time.Second), 3, 108405, 108407))
	if err != nil || len(fills) != 1 || fills[0].Price != 108408 {
		t.Fatalf("eligible quote did not fill with current price: %+v err=%v", fills, err)
	}
}

func TestQuoteLiquidityProducesPartialFillsAndProtectiveExitCancelsRemainder(t *testing.T) {
	t0 := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	manual := clock.NewManual(t0)
	broker, err := New(paperConfig(), manual)
	if err != nil {
		t.Fatal(err)
	}
	approved := approvedMarket(t0, "partial")
	approved.Intent.Quantity = 3000
	order, err := broker.Submit(context.Background(), approved)
	if err != nil || order.Status != domain.OrderAcknowledged {
		t.Fatalf("submit=%+v err=%v", order, err)
	}
	fills, err := broker.OnQuoteWithLiquidity(paperQuote(t0.Add(time.Second), 1, 108400, 108402), 1000)
	if err != nil || len(fills) != 1 || fills[0].Quantity != 1000 {
		t.Fatalf("first partial fill=%+v err=%v", fills, err)
	}
	orders := broker.Orders()
	positions, _ := broker.Positions(context.Background())
	if orders[0].Status != domain.OrderPartiallyFilled || orders[0].Remaining != 2000 ||
		len(positions) != 1 || positions[0].Quantity != 1000 {
		t.Fatalf("partial state orders=%+v positions=%+v", orders, positions)
	}
	exits, err := broker.OnQuoteWithLiquidity(paperQuote(t0.Add(2*time.Second), 2, 108389, 108391), 1)
	if err != nil || len(exits) != 1 || exits[0].Role != domain.FillStopLoss || exits[0].Quantity != 1000 {
		t.Fatalf("partial protective exit=%+v err=%v", exits, err)
	}
	orders = broker.Orders()
	if orders[0].Status != domain.OrderCancelled || orders[0].Remaining != 2000 {
		t.Fatalf("protective exit did not cancel remainder: %+v", orders[0])
	}
}

func TestClosedMarketAndExcessiveLatencyRejectOrders(t *testing.T) {
	t0 := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	manual := clock.NewManual(t0)
	broker, _ := New(paperConfig(), manual)
	if err := broker.SetMarketOpen("EURUSD", false, t0); err != nil {
		t.Fatal(err)
	}
	order, err := broker.Submit(context.Background(), approvedMarket(t0, "closed"))
	if err != nil || order.Status != domain.OrderRejected || order.RejectReason != "MARKET_CLOSED" {
		t.Fatalf("closed-market order=%+v err=%v", order, err)
	}

	config := paperConfig()
	instrument := config.Instruments["EURUSD"]
	instrument.SubmissionLatency = 2 * time.Minute
	config.Instruments["EURUSD"] = instrument
	delayed, _ := New(config, manual)
	order, err = delayed.Submit(context.Background(), approvedMarket(t0, "expired-by-latency"))
	if err != nil || order.Status != domain.OrderRejected || order.RejectReason != "LATENCY_EXCEEDS_INTENT_TTL" {
		t.Fatalf("latency-rejected order=%+v err=%v", order, err)
	}
}

func TestRolloverAccruesConfiguredTripleDayIntoClosedTrade(t *testing.T) {
	t0 := time.Date(2026, 8, 18, 21, 0, 0, 0, time.UTC) // Tuesday
	config := paperConfig()
	instrument := config.Instruments["EURUSD"]
	instrument.Rollover = RolloverConfig{
		Enabled: true, MinuteUTC: 22 * 60,
		ChargeWeekdays: [7]bool{false, true, true, true, true, true, false},
		TripleWeekday:  time.Wednesday, TripleMultiplier: 3,
		LongPerQuantityStepMinor: -1, ShortPerQuantityStepMinor: 1,
	}
	config.Instruments["EURUSD"] = instrument
	manual := clock.NewManual(t0.Add(time.Second))
	broker, err := New(config, manual)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := broker.OnQuote(paperQuote(t0, 1, 108400, 108402)); err != nil {
		t.Fatal(err)
	}
	approved := approvedMarket(t0.Add(time.Second), "rollover")
	approved.Intent.Quantity = 2000
	if _, err := broker.Submit(context.Background(), approved); err != nil {
		t.Fatal(err)
	}
	exits, err := broker.OnQuote(paperQuote(
		time.Date(2026, 8, 19, 22, 0, 1, 0, time.UTC), 2, 108430, 108432,
	))
	if err != nil || len(exits) != 1 {
		t.Fatalf("rollover exit=%+v err=%v", exits, err)
	}
	// Two steps pay -2 Tuesday and triple -6 Wednesday.
	if exits[0].RolloverMinor != -8 || exits[0].ClosedTradeNetMinor != exits[0].RealizedGrossMinor-8-8 {
		t.Fatalf("rollover was not included in net P&L: %+v", exits[0])
	}
}

func TestCandleSpreadCanVaryByEvent(t *testing.T) {
	t0 := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	broker, _ := New(paperConfig(), clock.Fixed{Time: t0.Add(time.Second)})
	_, _ = broker.OnQuote(paperQuote(t0, 1, 108400, 108402))
	if _, err := broker.Submit(context.Background(), approvedMarket(t0.Add(time.Second), "variable-spread")); err != nil {
		t.Fatal(err)
	}
	first := domain.Candle{
		Source: "paper-feed", Symbol: "EURUSD", Timeframe: time.Minute,
		OpenTime: t0.Add(time.Minute), CloseTime: t0.Add(2 * time.Minute),
		Open: 108420, High: 108432, Low: 108410, Close: 108425, TickCount: 10,
	}
	if fills, err := broker.OnCandleWithSpread(first, 3); err != nil || len(fills) != 0 {
		t.Fatalf("wide-spread candle unexpectedly filled: %+v err=%v", fills, err)
	}
	second := first
	second.OpenTime = first.CloseTime
	second.CloseTime = second.OpenTime.Add(time.Minute)
	fills, err := broker.OnCandleWithSpread(second, 1)
	if err != nil || len(fills) != 1 || fills[0].Role != domain.FillTakeProfit {
		t.Fatalf("narrow-spread candle did not fill: %+v err=%v", fills, err)
	}
}
