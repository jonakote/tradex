package economics

import (
	"testing"
	"time"

	"tradex/internal/domain"
)

func TestPriceValuesForEURUSDUSDJPYAndXAUUSD(t *testing.T) {
	observedAt := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		instrument domain.Instrument
		conversion *Rate
		wantNum    int64
		wantDen    int64
	}{
		{
			name:       "EURUSD",
			instrument: domain.Instrument{Symbol: "EURUSD", BaseCurrency: "EUR", QuoteCurrency: "USD", PriceScale: 100000, TickSize: 1, QuantityStep: 1000, MinQuantity: 1000, MaxQuantity: 1000000},
			wantNum:    1, wantDen: 1,
		},
		{
			name:       "USDJPY",
			instrument: domain.Instrument{Symbol: "USDJPY", BaseCurrency: "USD", QuoteCurrency: "JPY", PriceScale: 1000, TickSize: 1, QuantityStep: 1000, MinQuantity: 1000, MaxQuantity: 1000000},
			conversion: &Rate{From: "JPY", To: "USD", Numerator: 6800, Denominator: 1000000, Source: "USDJPY", ObservedAt: observedAt},
			wantNum:    17, wantDen: 25,
		},
		{
			name:       "XAUUSD",
			instrument: domain.Instrument{Symbol: "XAUUSD", BaseCurrency: "XAU", QuoteCurrency: "USD", PriceScale: 100, TickSize: 1, QuantityStep: 1, MinQuantity: 1, MaxQuantity: 1000},
			wantNum:    1, wantDen: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value, err := NewPriceValue(test.instrument, "USD", 100, test.conversion)
			if err != nil {
				t.Fatal(err)
			}
			if value.Numerator != test.wantNum || value.Denominator != test.wantDen {
				t.Fatalf("got %d/%d want %d/%d", value.Numerator, value.Denominator, test.wantNum, test.wantDen)
			}
		})
	}
}

func TestConvertedPriceValueRejectsStaleRate(t *testing.T) {
	now := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	instrument := domain.Instrument{Symbol: "USDJPY", BaseCurrency: "USD", QuoteCurrency: "JPY", PriceScale: 1000, TickSize: 1, QuantityStep: 1000, MinQuantity: 1000, MaxQuantity: 1000000}
	value, err := NewPriceValue(instrument, "USD", 100, &Rate{
		From: "JPY", To: "USD", Numerator: 6800, Denominator: 1000000,
		Source: "USDJPY", ObservedAt: now.Add(-time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := value.ValidateAt(now, 5*time.Second); err == nil {
		t.Fatal("stale conversion accepted")
	}
	if err := value.ValidateAt(now, 2*time.Minute); err != nil {
		t.Fatalf("fresh conversion rejected: %v", err)
	}
}

func TestLossRoundsUpAndRewardRoundsDownAfterFullQuantity(t *testing.T) {
	value := PriceValue{AccountCurrency: "USD", Numerator: 17, Denominator: 25}
	loss, err := value.LossMinor(1, 1000, 1000)
	if err != nil {
		t.Fatal(err)
	}
	reward, err := value.RewardMinor(1, 1000, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if loss != 1 || reward != 0 {
		t.Fatalf("loss/reward rounding got %d/%d want 1/0", loss, reward)
	}
	pnl, err := value.PnLMinor(10, 3000, 1000)
	if err != nil || pnl != 20 { // 10 * 3 * 17/25 = 20.4, toward zero.
		t.Fatalf("PnL got %d err=%v want 20", pnl, err)
	}
}
